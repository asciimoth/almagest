use tauri::{
    App, AppHandle, Manager, Runtime, WebviewWindow, Window, WindowEvent,
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
};

use crate::connection::AdminConnection;

const MAIN_WINDOW_LABEL: &str = "main";
const SHOW_MENU_ID: &str = "show";
const QUIT_MENU_ID: &str = "quit";

struct TrayAvailability(bool);

pub(crate) fn setup(app: &mut App, background: bool) -> Result<(), Box<dyn std::error::Error>> {
    let status_item = MenuItem::with_id(
        app,
        "connection-status",
        status_text(false),
        false,
        None::<&str>,
    )?;
    let show_item = MenuItem::with_id(app, SHOW_MENU_ID, "Show Almagest", true, None::<&str>)?;
    let quit_item = MenuItem::with_id(app, QUIT_MENU_ID, "Quit", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&status_item, &show_item, &quit_item])?;

    let mut builder = TrayIconBuilder::new()
        .menu(&menu)
        .show_menu_on_left_click(false)
        .tooltip(tooltip_text(false))
        .on_menu_event(|app, event| match event.id().as_ref() {
            SHOW_MENU_ID => show_main_window(app),
            QUIT_MENU_ID => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if matches!(
                event,
                TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                }
            ) {
                show_main_window(tray.app_handle());
            }
        });
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }
    let tray = match builder.build(app) {
        Ok(tray) => Some(tray),
        Err(error) => {
            crate::logging::write(format_args!("system tray is unavailable: {error}"));
            None
        }
    };
    app.manage(TrayAvailability(tray.is_some()));

    if let Some(tray) = tray {
        // Register after setup returns to the event loop. Registration sends
        // the current state immediately. Menu and tray setters wait for the
        // event loop, so calling them synchronously during setup deadlocks on
        // Windows.
        let app_handle = app.handle().clone();
        app.run_on_main_thread(move || {
            if let Err(error) =
                app_handle
                    .state::<AdminConnection>()
                    .set_status_handler(move |connected| {
                        if let Err(error) = status_item.set_text(status_text(connected)) {
                            crate::logging::write(format_args!(
                                "update tray connection status: {error}"
                            ));
                        }
                        if let Err(error) = tray.set_tooltip(Some(tooltip_text(connected))) {
                            crate::logging::write(format_args!("update tray tooltip: {error}"));
                        }
                    })
            {
                crate::logging::write(format_args!("register tray connection status: {error}"));
            }
        })?;
    }
    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
        if !background || !app.state::<TrayAvailability>().0 {
            restore_window(&window);
        }
        update_webview_retry_priority(&window);
    }
    Ok(())
}

pub(crate) fn handle_window_event(window: &Window, event: &WindowEvent) {
    if window.label() != MAIN_WINDOW_LABEL {
        return;
    }
    match event {
        WindowEvent::CloseRequested { api, .. } if window.state::<TrayAvailability>().0 => {
            api.prevent_close();
            hide_window(window);
        }
        WindowEvent::Resized(_) if window.is_minimized().unwrap_or(false) => hide_window(window),
        _ => {}
    }
    update_window_retry_priority(window);
}

fn hide_window<R: Runtime>(window: &Window<R>) {
    if let Err(error) = window.hide() {
        crate::logging::write(format_args!("hide main window: {error}"));
    }
}

fn show_main_window<R: Runtime>(app: &AppHandle<R>) {
    let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) else {
        return;
    };
    restore_window(&window);
    update_webview_retry_priority(&window);
}

fn restore_window<R: Runtime>(window: &WebviewWindow<R>) {
    if let Err(error) = window.unminimize() {
        crate::logging::write(format_args!("restore main window: {error}"));
    }
    if let Err(error) = window.show() {
        crate::logging::write(format_args!("show main window: {error}"));
    }
    if let Err(error) = window.set_focus() {
        crate::logging::write(format_args!("focus main window: {error}"));
    }
}

/// Use native window state to select the retry rate. No visibility or focus
/// information is added to the frontend IPC surface.
fn update_window_retry_priority<R: Runtime>(window: &Window<R>) {
    let interactive = window.is_visible().unwrap_or(false) && window.is_focused().unwrap_or(false);
    window
        .state::<AdminConnection>()
        .set_interactive(interactive);
}

fn update_webview_retry_priority<R: Runtime>(window: &WebviewWindow<R>) {
    let interactive = window.is_visible().unwrap_or(false) && window.is_focused().unwrap_or(false);
    window
        .state::<AdminConnection>()
        .set_interactive(interactive);
}

fn status_text(connected: bool) -> &'static str {
    if connected {
        "Admin API: Connected"
    } else {
        "Admin API: Disconnected"
    }
}

fn tooltip_text(connected: bool) -> &'static str {
    if connected {
        "Almagest — Admin API connected"
    } else {
        "Almagest — Admin API disconnected"
    }
}
