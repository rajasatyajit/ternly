use semver::{Version, VersionReq};

fn m(req: &str, v: &str) -> bool {
    VersionReq::parse(req).unwrap().matches(&Version::parse(v).unwrap())
}

#[test]
fn oracle_caret() {
    assert!(m("^1.2", "1.2.0"));
    assert!(m("^1.2", "1.9.9"));
    assert!(!m("^1.2", "1.1.9"));
    assert!(!m("^1.2", "2.0.0"));
    assert!(m("^0.2", "0.2.5"));
    assert!(!m("^0.2", "0.3.0"));
    assert!(m("^1.2.3", "1.2.3"));
    assert!(!m("^1.2.3", "1.2.2"));
}
