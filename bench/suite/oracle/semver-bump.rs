use semver::{BuildMetadata, Prerelease, Version};

#[test]
fn oracle_bump() {
    let v = Version::parse("1.4.7-rc.1+build.5").unwrap();
    let minor = v.bump_minor();
    assert_eq!(minor, Version::parse("1.5.0").unwrap());
    assert_eq!(minor.pre, Prerelease::EMPTY);
    assert_eq!(minor.build, BuildMetadata::EMPTY);
    let patch = v.bump_patch();
    assert_eq!(patch, Version::parse("1.4.8").unwrap());
    assert_eq!(patch.build, BuildMetadata::EMPTY);
    assert_eq!(v, Version::parse("1.4.7-rc.1+build.5").unwrap());
    assert_eq!(Version::new(0, 0, 9).bump_minor(), Version::new(0, 1, 0));
}
