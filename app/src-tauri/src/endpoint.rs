use std::path::{Component, Path, PathBuf};

use percent_encoding::percent_decode_str;
use url::Url;

/// A normalized administration endpoint accepted by `almagest-cli`.
#[derive(Debug, Clone, Eq, PartialEq)]
pub(crate) enum Endpoint {
    Unix(PathBuf),
    Pipe { trust: PipeTrust, name: String },
    Web(Url),
}

#[derive(Debug, Clone, Copy, Eq, PartialEq)]
pub(crate) enum PipeTrust {
    User,
    Machine,
}

impl PipeTrust {
    #[cfg(windows)]
    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::User => "user",
            Self::Machine => "machine",
        }
    }
}

/// Parse the endpoint URL syntax shared with `almagest-cli`.
pub(crate) fn parse(raw: &str) -> Result<Endpoint, String> {
    let raw = if raw.is_empty() { default_url() } else { raw };
    let url = Url::parse(raw).map_err(|error| format!("parse endpoint: {error}"))?;

    match url.scheme() {
        "unix" => parse_unix(&url),
        "pipe" => parse_pipe(raw, &url),
        "web" => parse_web(&url),
        scheme => Err(format!("unsupported endpoint scheme {scheme:?}")),
    }
}

#[cfg(unix)]
const fn default_url() -> &'static str {
    "unix://run/almagest/admin.sock"
}

#[cfg(windows)]
const fn default_url() -> &'static str {
    "pipe://machine"
}

#[cfg(not(any(unix, windows)))]
const fn default_url() -> &'static str {
    "unix://run/almagest/admin.sock"
}

fn parse_unix(url: &Url) -> Result<Endpoint, String> {
    let encoded_path = match url.host_str() {
        Some(host) => format!("/{host}{}", url.path()),
        None => url.path().to_owned(),
    };
    let path = percent_decode_str(&encoded_path)
        .decode_utf8()
        .map_err(|error| format!("unix endpoint path: {error}"))?
        .into_owned();
    if path.is_empty() {
        return Err("unix endpoint path is empty".to_owned());
    }
    let path = Path::new(&path);
    if !path.is_absolute() {
        return Err(format!(
            "unix endpoint path must be absolute: {}",
            path.display()
        ));
    }
    Ok(Endpoint::Unix(clean_absolute_path(path)))
}

fn clean_absolute_path(path: &Path) -> PathBuf {
    let mut clean = PathBuf::new();
    for component in path.components() {
        match component {
            Component::ParentDir => {
                clean.pop();
            }
            Component::CurDir => {}
            other => clean.push(other.as_os_str()),
        }
    }
    clean
}

fn parse_web(url: &Url) -> Result<Endpoint, String> {
    if url.host_str().is_none() {
        return Err("web endpoint host is empty".to_owned());
    }
    // `web` is not a WHATWG special scheme, while `ws` is. Reparse instead
    // of using set_scheme, which rejects a change between those categories.
    let mut url = Url::parse(&format!("ws:{}", &url.as_str()["web:".len()..]))
        .map_err(|error| format!("convert web endpoint to WebSocket URL: {error}"))?;
    if url.path().is_empty() || url.path() == "/" {
        url.set_path("/api/ws");
    }
    Ok(Endpoint::Web(url))
}

fn parse_pipe(raw: &str, url: &Url) -> Result<Endpoint, String> {
    if !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err(
            "pipe endpoint must not contain credentials, a query, or a fragment".to_owned(),
        );
    }
    let trust = match url.host_str() {
        Some("user") => PipeTrust::User,
        Some("machine") => PipeTrust::Machine,
        _ => return Err("pipe endpoint host must be \"user\" or \"machine\"".to_owned()),
    };

    // Use the original path because URL parsers normalize `..` segments. A
    // pipe name is one component, so normalization must not make it safe.
    let after_scheme = raw
        .split_once(':')
        .map(|(_scheme, remainder)| remainder)
        .and_then(|remainder| remainder.strip_prefix("//"))
        .ok_or_else(|| "pipe endpoint must use pipe://".to_owned())?;
    let raw_path = after_scheme
        .find('/')
        .map_or("", |position| &after_scheme[position + 1..]);
    let name = percent_decode_str(raw_path)
        .decode_utf8()
        .map_err(|error| format!("pipe endpoint name: {error}"))?
        .into_owned();
    if matches!(name.as_str(), "." | "..") || name.contains(['/', '\\']) {
        return Err("pipe endpoint name must be one local pipe component".to_owned());
    }
    Ok(Endpoint::Pipe { trust, name })
}

#[cfg(test)]
mod tests {
    use super::{Endpoint, PipeTrust, default_url, parse};
    use std::path::PathBuf;

    #[test]
    fn platform_default_matches_cli() {
        let endpoint = parse("").expect("default endpoint must parse");
        #[cfg(unix)]
        assert_eq!(
            endpoint,
            Endpoint::Unix(PathBuf::from("/run/almagest/admin.sock"))
        );
        #[cfg(windows)]
        assert_eq!(
            endpoint,
            Endpoint::Pipe {
                trust: PipeTrust::Machine,
                name: String::new()
            }
        );
        assert_ne!(default_url(), "");
    }

    #[test]
    fn parses_unix_and_web_endpoints() {
        assert_eq!(
            parse("unix:/tmp/../tmp/admin.sock"),
            Ok(Endpoint::Unix(PathBuf::from("/tmp/admin.sock")))
        );
        let Endpoint::Web(url) = parse("web://127.0.0.1:8090").expect("web endpoint") else {
            panic!("wrong endpoint kind");
        };
        assert_eq!(url.as_str(), "ws://127.0.0.1:8090/api/ws");

        let Endpoint::Web(url) =
            parse("web://example.test/custom?token=value").expect("custom web endpoint")
        else {
            panic!("wrong endpoint kind");
        };
        assert_eq!(url.as_str(), "ws://example.test/custom?token=value");
    }

    #[test]
    fn parses_pipe_trust_and_names() {
        assert_eq!(
            parse("pipe://user/test-instance"),
            Ok(Endpoint::Pipe {
                trust: PipeTrust::User,
                name: "test-instance".to_owned()
            })
        );
        assert_eq!(
            parse("pipe://machine/test%20instance"),
            Ok(Endpoint::Pipe {
                trust: PipeTrust::Machine,
                name: "test instance".to_owned()
            })
        );
    }

    #[test]
    fn rejects_invalid_endpoints() {
        for raw in [
            "admin.sock",
            "https://example.test",
            "web:/api/ws",
            "unix:relative.sock",
            "pipe://other",
            "pipe://user/../other",
            "pipe://user/a%2Fb",
            "pipe://machine/name?owner=user",
        ] {
            assert!(parse(raw).is_err(), "accepted {raw:?}");
        }
    }
}
