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

#[tauri::command]
#[allow(clippy::needless_pass_by_value)]
fn log_desktop_event(event: String) -> Result<(), String> {
    if !is_desktop_event(&event) {
        return Err("unknown desktop diagnostic event".into());
    }
    #[cfg(windows)]
    logging::write(format_args!("desktop frontend [windows]: {event}"));
    #[cfg(not(windows))]
    logging::write(format_args!("desktop frontend: {event}"));
    Ok(())
}

fn is_desktop_event(event: &str) -> bool {
    matches!(
        event,
        "docs_frame_created"
            | "docs_frame_loaded"
            | "docs_frame_ready"
            | "docs_frame_reconnected"
            | "docs_frame_timeout"
    )
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

    let builder = tauri::Builder::default();
    // This plugin must be registered first so a later process exits before
    // other plugins or application setup run.
    #[cfg(any(target_os = "linux", target_os = "macos", windows))]
    let builder = builder.plugin(tauri_plugin_single_instance::init(
        |app, _arguments, _working_directory| {
            logging::write(format_args!(
                "desktop app activation forwarded by another instance"
            ));
            tray::show_main_window(app);
        },
    ));

    let connection = AdminConnection::connect(&endpoint);
    builder
        // The opener plugin is used only from trusted Rust. No opener command
        // is present in the frontend capability.
        .plugin(tauri_plugin_opener::init())
        .plugin(navigation::plugin())
        .manage(connection)
        .setup(move |app| tray::setup(app, background))
        .on_window_event(tray::handle_window_event)
        .invoke_handler(tauri::generate_handler![
            listen_admin,
            send_admin_message,
            log_desktop_event
        ])
        .run(tauri::generate_context!())?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::is_desktop_event;

    #[test]
    fn desktop_log_accepts_only_fixed_events() {
        for event in [
            "docs_frame_created",
            "docs_frame_loaded",
            "docs_frame_ready",
            "docs_frame_reconnected",
            "docs_frame_timeout",
        ] {
            assert!(is_desktop_event(event));
        }
        assert!(!is_desktop_event("arbitrary message"));
        assert!(!is_desktop_event("docs_frame_ready\nforged log line"));
    }
}
