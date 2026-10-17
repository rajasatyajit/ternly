use semver::{Version, VersionReq};

fn m(req: &str, v: &str) -> bool {
    VersionReq::parse(req).unwrap().matches(&Version::parse(v).unwrap())
}

#[test]
fn oracle_tilde() {
    assert!(m("~1.2.3", "1.2.3"));
    assert!(m("~1.2.3", "1.2.5"));
    assert!(!m("~1.2.3", "1.2.1"));
    assert!(!m("~1.2.3", "1.3.0"));
    assert!(m("~1.2", "1.2.0"));
    assert!(!m("~1.2", "1.3.0"));
}
