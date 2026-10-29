#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod connection;
mod endpoint;
mod logging;
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
    let result = connection.send(message);
    if let Err(error) = &result {
        logging::write(format_args!("panel admin IPC: request rejected: {error}"));
    }
    result
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    match logging::initialize() {
        Ok(path) => logging::write(format_args!(
            "desktop app starting pid={} log={}",
            std::process::id(),
            path.display()
        )),
        Err(error) => eprintln!("initialize desktop app logging: {error}"),
    }
    let default_panic_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |information| {
        logging::write(format_args!("desktop app panic: {information}"));
        default_panic_hook(information);
    }));
    let result = run();
    match &result {
        Ok(()) => logging::write(format_args!("desktop app stopped")),
        Err(error) => logging::write(format_args!("desktop app failed: {error}")),
    }
    result
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    let options = options::parse(
        std::env::args_os().skip(1),
        std::env::var_os(options::API_URL_ENV),
    )?;
    let Options::Connect {
        endpoint,
        background,
    } = options
    else {
        print!("{}", options::HELP);
        return Ok(());
    };
    logging::write(format_args!(
        "desktop app options endpoint_kind={} background={background}",
        endpoint.diagnostic_kind()
    ));

    let connection = AdminConnection::connect(&endpoint);
    tauri::Builder::default()
        // The opener plugin is used only from trusted Rust. No opener command
        // is present in the frontend capability.
        .plugin(tauri_plugin_opener::init())
        .plugin(navigation::plugin())
        .manage(connection)
        .setup(move |app| tray::setup(app, background))
        .on_window_event(tray::handle_window_event)
        .invoke_handler(tauri::generate_handler![listen_admin, send_admin_message])
        .run(tauri::generate_context!())?;
    Ok(())
}
