use std::{env, fs, path::Path};

fn main() {
    let out = env::var("OUT_DIR").unwrap();
    fs::write(
        Path::new(&out).join("made.rs"),
        "pub struct Made;\n\nimpl Made {\n    pub fn new() -> Made {\n        Made\n    }\n}\n",
    )
    .unwrap();
}
