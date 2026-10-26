use std::{
    error::Error,
    fs, io,
    path::{Path, PathBuf},
};

use resvg::{
    tiny_skia::{Pixmap, Transform},
    usvg::{Options, Tree},
};

const ICONS: &[(&str, u16)] = &[
    ("32x32.png", 32),
    ("128x128.png", 128),
    ("128x128@2x.png", 256),
    ("icon.png", 512),
];

fn main() -> Result<(), Box<dyn Error>> {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let logo_path = manifest_dir.join("../../logo-light.svg");
    let logo = fs::read(&logo_path)?;

    println!("cargo:rerun-if-changed={}", logo_path.display());

    write_if_changed(&manifest_dir.join("../frontend/logo-light.svg"), &logo)?;

    let tree = Tree::from_data(&logo, &Options::default())?;
    for &(name, size) in ICONS {
        let png = render_png(&tree, size)?;
        write_if_changed(&manifest_dir.join("icons").join(name), &png)?;
    }

    tauri_build::build();
    Ok(())
}

fn render_png(tree: &Tree, size: u16) -> Result<Vec<u8>, Box<dyn Error>> {
    let pixel_size = u32::from(size);
    let mut pixmap = Pixmap::new(pixel_size, pixel_size)
        .ok_or_else(|| io::Error::other(format!("cannot allocate a {size}x{size} icon")))?;
    let source_size = tree.size();
    let transform = Transform::from_scale(
        f32::from(size) / source_size.width(),
        f32::from(size) / source_size.height(),
    );

    resvg::render(tree, transform, &mut pixmap.as_mut());
    Ok(pixmap.encode_png()?)
}

fn write_if_changed(path: &Path, contents: &[u8]) -> io::Result<()> {
    if fs::read(path).is_ok_and(|current| current == contents) {
        return Ok(());
    }

    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    fs::write(path, contents)
}
