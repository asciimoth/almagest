use tauri::{Manager, Runtime, plugin::TauriPlugin};
use tauri_plugin_opener::OpenerExt;
use url::Url;

#[derive(Debug, Eq, PartialEq)]
enum Navigation {
    Internal,
    External,
    Blocked,
}

/// Install a native navigation boundary without adding a frontend permission.
/// Packaged assets stay in the single webview. Safe external links use the OS
/// default application. Other schemes never leave the application boundary.
pub(crate) fn plugin<R: Runtime>() -> TauriPlugin<R> {
    tauri::plugin::Builder::new("navigation-boundary")
        .on_navigation(|webview, url| match classify(url) {
            Navigation::Internal => {
                #[cfg(windows)]
                if url.path().starts_with("/docs/") {
                    crate::logging::write(format_args!(
                        "webview navigation [windows]: loading internal documentation frame path={}",
                        url.path()
                    ));
                }
                true
            }
            Navigation::External => {
                if let Err(error) = webview
                    .app_handle()
                    .opener()
                    .open_url(url.as_str(), None::<&str>)
                {
                    crate::logging::write(format_args!("open external URL: {error}"));
                }
                false
            }
            Navigation::Blocked => {
                crate::logging::write(format_args!(
                    "blocked webview navigation to scheme {}",
                    url.scheme()
                ));
                false
            }
        })
        .build()
}

fn classify(url: &Url) -> Navigation {
    if url.scheme() == "tauri"
        || matches!(url.scheme(), "http" | "https") && url.host_str() == Some("tauri.localhost")
        || cfg!(dev)
            && url.scheme() == "http"
            && matches!(url.host_str(), Some("localhost" | "127.0.0.1" | "::1"))
    {
        return Navigation::Internal;
    }
    if matches!(url.scheme(), "http" | "https" | "mailto") {
        return Navigation::External;
    }
    Navigation::Blocked
}

#[cfg(test)]
mod tests {
    use super::{Navigation, classify};

    fn url(value: &str) -> url::Url {
        url::Url::parse(value).expect("test URL")
    }

    #[test]
    fn packaged_pages_stay_in_the_webview() {
        assert_eq!(
            classify(&url("tauri://localhost/docs/index.html")),
            Navigation::Internal
        );
        assert_eq!(
            classify(&url("http://tauri.localhost/index.html")),
            Navigation::Internal
        );
        assert_eq!(
            classify(&url("http://127.0.0.1:1430/docs/index.html")),
            if cfg!(dev) {
                Navigation::Internal
            } else {
                Navigation::External
            }
        );
    }

    #[test]
    fn safe_external_links_use_the_system_handler() {
        assert_eq!(
            classify(&url("https://example.com/docs")),
            Navigation::External
        );
        assert_eq!(
            classify(&url("mailto:maintainer@example.com")),
            Navigation::External
        );
    }

    #[test]
    fn privileged_and_unknown_schemes_are_blocked() {
        assert_eq!(classify(&url("about:blank")), Navigation::Blocked);
        assert_eq!(classify(&url("about:srcdoc")), Navigation::Blocked);
        assert_eq!(classify(&url("file:///etc/passwd")), Navigation::Blocked);
        assert_eq!(classify(&url("javascript:alert(1)")), Navigation::Blocked);
        assert_eq!(classify(&url("custom://example")), Navigation::Blocked);
    }
}
