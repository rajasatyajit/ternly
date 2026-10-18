use semver::Version;

fn v(s: &str) -> Version {
    Version::parse(s).unwrap()
}

#[test]
fn oracle_prerelease_order() {
    assert!(v("1.0.0-alpha.2") < v("1.0.0-alpha.10"));
    assert!(v("1.0.0-rc.9") < v("1.0.0-rc.11"));
    assert!(v("1.0.0-1") < v("1.0.0-2"));
    assert!(v("1.0.0-9") < v("1.0.0-10"));
    assert!(v("1.0.0-alpha") < v("1.0.0-alpha.1"));
    assert!(v("1.0.0-alpha.1") < v("1.0.0-alpha.beta"));
    assert!(v("1.0.0-alpha.beta") < v("1.0.0-beta"));
    assert!(v("1.0.0-beta.11") < v("1.0.0-rc.1"));
    assert!(v("1.0.0-rc.1") < v("1.0.0"));
    let mut vs = vec![v("1.0.0-alpha.10"), v("1.0.0-alpha.2"), v("1.0.0-alpha.1")];
    vs.sort();
    assert_eq!(vs, vec![v("1.0.0-alpha.1"), v("1.0.0-alpha.2"), v("1.0.0-alpha.10")]);
}
