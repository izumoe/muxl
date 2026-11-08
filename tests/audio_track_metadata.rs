use std::collections::BTreeMap;
use std::io::Cursor;

use muxl::catalog::{TrackMeta, TrackOptions};
use muxl::{Segmenter, SegmenterEvent, segment_fmp4_with_options};

fn source() -> Vec<u8> {
    std::fs::read(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/samples/fixtures/h264-opus-frag.mp4"
    ))
    .unwrap()
}

#[test]
fn track_metadata_follows_remap_through_both_segmenters() {
    let options = TrackOptions {
        remap: BTreeMap::from([(2, 9)]),
        track_meta: BTreeMap::from([(
            9,
            TrackMeta {
                alternate_group: 3,
                label: "Music".into(),
            },
        )]),
        ..Default::default()
    };
    let input = source();
    let mut pull = Vec::new();
    let catalog = segment_fmp4_with_options(&mut Cursor::new(&input), options.clone(), |gop| {
        pull.push(gop);
        Ok(())
    })
    .unwrap();
    let audio = catalog.audio_configs().next().unwrap();
    assert_eq!(
        (
            audio.track_id(),
            audio.alternate_group,
            audio.label.as_deref()
        ),
        (9, Some(3), Some("Music"))
    );

    let mut push = Segmenter::with_options(options);
    let mut events = Vec::new();
    for chunk in input.chunks(113) {
        events.extend(push.feed(chunk).unwrap());
    }
    events.extend(push.flush().unwrap());
    let mut actual = Vec::new();
    for event in events {
        match event {
            SegmenterEvent::InitSegment { catalog: init, .. } => assert_eq!(init, catalog),
            SegmenterEvent::Segment(gop) => actual.push(gop),
        }
    }
    assert_eq!(actual, pull);
    let mut found = false;
    for gop in &actual {
        if let Some(track) = gop.tracks.get(&9) {
            let track_catalog = muxl::catalog::from_segment(track).unwrap();
            let audio = track_catalog.audio_configs().next().unwrap();
            assert_eq!(
                (audio.alternate_group, audio.label.as_deref()),
                (Some(3), Some("Music"))
            );
            found = true;
        }
    }
    assert!(
        found,
        "the remapped audio track must appear in canonical segments"
    );
}

#[test]
fn metadata_for_missing_or_non_audio_tracks_is_rejected() {
    for id in [1, 99] {
        let options = TrackOptions {
            track_meta: BTreeMap::from([(
                id,
                TrackMeta {
                    alternate_group: 1,
                    label: "Mix".into(),
                },
            )]),
            ..Default::default()
        };
        let mut push = Segmenter::with_options(options);
        assert!(
            push.feed(&source()).is_err(),
            "metadata for track {id} must not be ignored"
        );
    }
}

#[test]
fn source_audio_metadata_is_used_only_when_requested() {
    let mut catalog = muxl::init::catalog_from_mp4(Cursor::new(source())).unwrap();
    let audio = catalog
        .audio
        .as_mut()
        .unwrap()
        .renditions
        .values_mut()
        .next()
        .unwrap();
    audio.alternate_group = Some(3);
    audio.label = Some("Music".into());
    let input = muxl::init::build_init_segment(&catalog).unwrap();
    for enabled in [false, true] {
        let options = TrackOptions {
            source_track_meta: enabled,
            ..Default::default()
        };
        let pulled =
            segment_fmp4_with_options(&mut Cursor::new(&input), options.clone(), |_| Ok(()))
                .unwrap();
        let mut segmenter = Segmenter::with_options(options);
        let events = segmenter.feed(&input).unwrap();
        let SegmenterEvent::InitSegment { catalog, .. } = &events[0] else {
            panic!("missing init")
        };
        assert_eq!(*catalog, pulled);
        let audio = catalog.audio_configs().next().unwrap();
        assert_eq!(audio.alternate_group, enabled.then_some(3));
        assert_eq!(audio.label.as_deref(), enabled.then_some("Music"));
    }
}
