//! Verify signed canonical MUXL segments with c2pa-rs, in-process.
//!
//! Streamplace previously verified provenance by calling the
//! `iroh-streamplace` uniffi binding's `get_manifest_and_cert` over a flat
//! signed MP4. With the canonical `.m4s` segment as the stored and
//! transmitted unit, verification moves here: [`unwrap`] the wrapper into
//! its per-track signed segments and run c2pa-rs's [`Reader`] over each one
//! as an `m4s` asset — the exact format it was signed as
//! (`sign::sign_buf_as(.., "m4s")`). A single `muxl.wasm` now covers
//! segment / sign / wrap / verify, so the c2pa dependency leaves the host.
//!
//! [`unwrap`]: muxl::reader::unwrap
//! [`Reader`]: c2pa::Reader

use std::collections::HashMap;
use std::io::Cursor;

use c2pa::{Manifest, Reader, Relationship, ValidationState};
use serde_json::json;
use x509_cert::Certificate;

use crate::error::{Error, Result};
use crate::sign::init_default_settings;

/// Verify every canonical segment in a signed MUXL wrapper. `bytes` may be a
/// bare `.m4s` stream, a MUXL fMP4, or a flat MP4 — [`muxl::reader::unwrap`]
/// fast-forwards over the framing and splits on segment boundaries, keeping
/// each segment's leading c2pa-uuid box attached.
///
/// Returns a JSON document, one entry per track, each mirroring the old
/// `get_manifest_and_cert` shape so the host's assertion- and cert-parsing
/// applies unchanged:
///
/// ```json
/// {"segments": [
///   {"track_id": 1,
///    "manifest": { .. }, "cert": "<pem chain>",
///    "validation_results": { .. }, "validation_state": ".."},
///   ..
/// ]}
/// ```
///
/// Cryptographic *failures* (a tampered segment) surface in each entry's
/// `validation_results` / `validation_state` for the caller to enforce —
/// matching how the host already inspects `validation_results.failure`. This
/// function errors only on structural problems: nothing to unwrap, an
/// unreadable segment, or a segment carrying no signed manifest.
pub fn verify_segments(bytes: &[u8]) -> Result<String> {
    init_default_settings();
    verify(bytes, false)
}

/// Verify source claims embedded in each derived segment as well as the
/// segment itself. Settings are scoped to this call and restored afterwards.
pub fn verify_segments_with_ingredients(bytes: &[u8]) -> Result<String> {
    init_default_settings();
    let original = c2pa::settings::Settings::to_toml()?;
    c2pa::settings::Settings::from_toml(
        "[verify]\nverify_after_reading = true\ncheck_ingredient_trust = true\n",
    )?;
    let result = verify(bytes, true);
    c2pa::settings::Settings::from_toml(&original)?;
    result
}

fn verify(bytes: &[u8], ingredients: bool) -> Result<String> {
    let segments = muxl::reader::unwrap(bytes)?;
    if segments.is_empty() {
        return Err(Error::Muxl(muxl::Error::InvalidMp4(
            "no canonical segments to verify".into(),
        )));
    }

    let mut out = Vec::with_capacity(segments.len());
    for seg in &segments {
        // Each segment was signed standalone as an "m4s" asset, so it
        // verifies standalone — no synthesized init/moov is involved (the
        // hash covers only s2pa(muxl(data)), never a header).
        let reader = Reader::from_stream("m4s", Cursor::new(seg.data))?;
        let manifest = reader.active_manifest().ok_or_else(|| {
            Error::Muxl(muxl::Error::InvalidMp4(format!(
                "canonical segment for track {} has no active c2pa manifest",
                seg.track_id
            )))
        })?;
        let cert_chain = manifest
            .signature_info()
            .map(|si| si.cert_chain().to_string())
            .ok_or_else(|| {
                Error::Muxl(muxl::Error::InvalidMp4(format!(
                    "track {} manifest carries no signature info",
                    seg.track_id
                )))
            })?;
        let mut result = json!({
            "track_id": seg.track_id,
            "manifest": manifest,
            "cert": cert_chain,
            "signer_pubkey": leaf_pubkey_hex(&cert_chain),
            "validation_results": reader.validation_results(),
            "validation_state": reader.validation_state(),
        });
        if ingredients {
            let active_signature_valid = reader
                .validation_results()
                .and_then(|results| results.active_manifest())
                .is_some_and(|status| {
                    status.success().iter().any(|entry| {
                        entry.code() == c2pa::validation_results::validation_codes::CLAIM_SIGNATURE_VALIDATED
                    })
                });
            if reader.validation_state() == ValidationState::Invalid || !active_signature_valid {
                return Err(Error::Muxl(muxl::Error::InvalidMp4(
                    "ingredient verification failed".into(),
                )));
            }
            let mut verified = HashMap::new();
            collect_ingredients(&reader, manifest, &mut verified)?;
            result["verified_ingredients"] = json!(verified);
        }
        out.push(result);
    }

    Ok(json!({ "segments": out }).to_string())
}

fn collect_ingredients(
    reader: &Reader,
    manifest: &Manifest,
    verified: &mut HashMap<String, serde_json::Value>,
) -> Result<()> {
    let value = serde_json::to_value(manifest).map_err(|e| Error::Io(std::io::Error::other(e)))?;
    if value
        .get("redactions")
        .and_then(|v| v.as_array())
        .is_some_and(|r| !r.is_empty())
    {
        return Err(Error::Muxl(muxl::Error::InvalidMp4(
            "redacted source manifest".into(),
        )));
    }
    for ingredient in manifest.ingredients() {
        if ingredient.relationship() == &Relationship::InputTo {
            return Err(Error::Muxl(muxl::Error::InvalidMp4(
                "unsupported input ingredient".into(),
            )));
        }
        if ingredient.validation_status().is_some_and(|statuses| {
            statuses.iter().any(|s| {
                c2pa::validation_results::validation_codes::log_kind(s.code())
                    == c2pa::status_tracker::LogKind::Failure
            })
        }) || ingredient
            .validation_results()
            .is_some_and(|results| results.validation_state() == ValidationState::Invalid)
        {
            return Err(Error::Muxl(muxl::Error::InvalidMp4(
                "ingredient has validation failures".into(),
            )));
        }
        let label = ingredient.active_manifest().ok_or_else(|| {
            Error::Muxl(muxl::Error::InvalidMp4(
                "ingredient has no source manifest".into(),
            ))
        })?;
        if verified.contains_key(label) {
            continue;
        }
        let source = reader.get_manifest(label).ok_or_else(|| {
            Error::Muxl(muxl::Error::InvalidMp4(
                "ingredient source manifest is missing".into(),
            ))
        })?;
        let cert = source
            .signature_info()
            .map(|info| info.cert_chain())
            .ok_or_else(|| {
                Error::Muxl(muxl::Error::InvalidMp4(
                    "ingredient source signature is missing".into(),
                ))
            })?;
        verified.insert(label.to_string(), json!({"manifest": source, "cert": cert}));
        collect_ingredients(reader, source, verified)?;
    }
    Ok(())
}

/// Extract the leaf cert's public key (uncompressed SEC1, lowercase hex) from a
/// PEM cert chain. This lets a host bind the S2PA signer to an external
/// identity — e.g. recover a Livepeer orchestrator's Ethereum address from the
/// secp256k1 key, confirming the signer is the orchestrator it selected.
/// Returns `None` if the chain doesn't parse.
fn leaf_pubkey_hex(cert_chain_pem: &str) -> Option<String> {
    // load_pem_chain tolerates one-or-many concatenated PEM certs (and
    // trailing whitespace), unlike the strict single-doc from_pem.
    let chain = Certificate::load_pem_chain(cert_chain_pem.as_bytes()).ok()?;
    let leaf = chain.first()?;
    let bytes = leaf
        .tbs_certificate
        .subject_public_key_info
        .subject_public_key
        .raw_bytes();
    Some(hex_lower(bytes))
}

fn hex_lower(b: &[u8]) -> String {
    let mut s = String::with_capacity(b.len() * 2);
    for byte in b {
        s.push_str(&format!("{byte:02x}"));
    }
    s
}
