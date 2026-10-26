#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod connection;
mod endpoint;
mod options;

use connection::AdminConnection;
use options::Options;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let options = options::parse(std::env::args_os().skip(1))?;
    let Options::Connect(endpoint) = options else {
        print!("{}", options::HELP);
        return Ok(());
    };

    let connection = AdminConnection::connect(&endpoint)?;
    tauri::Builder::default()
        .manage(connection)
        .run(tauri::generate_context!())?;
    Ok(())
}
