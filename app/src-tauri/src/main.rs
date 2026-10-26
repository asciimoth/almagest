#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod connection;
mod endpoint;
mod navigation;
mod options;
mod tray;

use connection::{AdminConnection, FrontendEvent};
use options::Options;

#[tauri::command]
#[allow(clippy::needless_pass_by_value)]
fn listen_admin(
    connection: tauri::State<'_, AdminConnection>,
    channel: tauri::ipc::Channel<FrontendEvent>,
) -> Result<(), String> {
    connection.register(channel)
}

#[tauri::command]
#[allow(clippy::needless_pass_by_value)]
fn send_admin_message(
    connection: tauri::State<'_, AdminConnection>,
    message: String,
) -> Result<(), String> {
    connection.send(message)
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let options = options::parse(
        std::env::args_os().skip(1),
        std::env::var_os(options::API_URL_ENV),
    )?;
    let Options::Connect(endpoint) = options else {
        print!("{}", options::HELP);
        return Ok(());
    };

    let connection = AdminConnection::connect(&endpoint);
    tauri::Builder::default()
        // The opener plugin is used only from trusted Rust. No opener command
        // is present in the frontend capability.
        .plugin(tauri_plugin_opener::init())
        .plugin(navigation::plugin())
        .manage(connection)
        .setup(tray::setup)
        .on_window_event(tray::handle_window_event)
        .invoke_handler(tauri::generate_handler![listen_admin, send_admin_message])
        .run(tauri::generate_context!())?;
    Ok(())
}
