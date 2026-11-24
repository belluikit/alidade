mod common;

fn helper() -> f64 {
    shapes::total(&[common::sample()])
}

#[test]
fn second() {
    assert!(helper() > 0.0);
}
