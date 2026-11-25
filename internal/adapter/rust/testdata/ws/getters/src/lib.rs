//! A derive that writes a getter named after its type's field `side`, as
//! prost's derive writes `name()` beside a field `name`: the method a
//! macro writes, sharing a field's name (ADR 002 rule 16).

use proc_macro::{TokenStream, TokenTree};

#[proc_macro_derive(Side)]
pub fn side(input: TokenStream) -> TokenStream {
    let mut tokens = input.into_iter();
    let mut name = String::new();
    while let Some(t) = tokens.next() {
        if let TokenTree::Ident(i) = &t {
            if i.to_string() == "struct" {
                if let Some(TokenTree::Ident(n)) = tokens.next() {
                    name = n.to_string();
                }
                break;
            }
        }
    }
    format!("impl {name} {{ pub fn side(&self) -> f64 {{ self.side }} }}")
        .parse()
        .unwrap()
}
