use std::ffi::OsString;

use crate::endpoint::{self, Endpoint};

pub(crate) const API_URL_ENV: &str = "ALMAGEST_API_URL";

pub(crate) const HELP: &str = "\
Usage:
  almagest-app [--background] [--url ENDPOINT]

Endpoint URLs:
  unix://run/almagest/admin.sock   Unix socket at /run/almagest/admin.sock (Unix default)
  unix:///absolute/path.sock       Unix socket at an explicit absolute path
  pipe://machine                   trusted machine administration pipe (Windows default)
  pipe://user                      trusted current-user pipe
  web://127.0.0.1:8090             WebSocket API at ws://127.0.0.1:8090/api/ws

Options:
      --background     start with the main window hidden when the tray is available
  -u, --url ENDPOINT   admin API endpoint URL
  -h, --help           show help

Environment:
  ALMAGEST_API_URL     admin API endpoint URL; overridden by -u or --url
";

#[derive(Debug, Eq, PartialEq)]
pub(crate) enum Options {
    Connect {
        endpoint: Endpoint,
        background: bool,
    },
    Help,
}

/// Parse only the global endpoint flags supported by `almagest-cli`.
pub(crate) fn parse(
    args: impl IntoIterator<Item = OsString>,
    environment_endpoint: Option<OsString>,
) -> Result<Options, String> {
    let mut args = args.into_iter();
    let mut raw_endpoint = None;
    let mut background = false;
    while let Some(argument) = args.next() {
        let argument = argument
            .into_string()
            .map_err(|_| "command-line arguments must be valid UTF-8".to_owned())?;
        match argument.as_str() {
            "-h" | "--help" => return Ok(Options::Help),
            "--background" => background = true,
            "-u" | "--url" => {
                let value = args
                    .next()
                    .ok_or_else(|| format!("{argument} requires an endpoint URL"))?
                    .into_string()
                    .map_err(|_| "endpoint URL must be valid UTF-8".to_owned())?;
                raw_endpoint = Some(value);
            }
            _ => {
                if let Some(value) = argument
                    .strip_prefix("--url=")
                    .or_else(|| argument.strip_prefix("-u="))
                {
                    raw_endpoint = Some(value.to_owned());
                } else {
                    return Err(format!("unknown argument {argument:?}"));
                }
            }
        }
    }
    let raw_endpoint = raw_endpoint
        .map(Ok)
        .or_else(|| {
            environment_endpoint.map(|value| {
                value
                    .into_string()
                    .map_err(|_| format!("{API_URL_ENV} must be valid UTF-8"))
            })
        })
        .transpose()?
        .unwrap_or_default();
    endpoint::parse(&raw_endpoint).map(|endpoint| Options::Connect {
        endpoint,
        background,
    })
}

#[cfg(test)]
mod tests {
    use super::{Options, parse};
    use crate::endpoint::Endpoint;
    use std::{ffi::OsString, path::PathBuf};

    fn args<'a>(values: &'a [&'a str]) -> impl Iterator<Item = OsString> + 'a {
        values.iter().map(OsString::from)
    }

    #[test]
    fn accepts_cli_url_flags() {
        for flag in ["-u", "--url"] {
            assert_eq!(
                parse(args(&[flag, "unix:///tmp/admin.sock"]), None),
                Ok(Options::Connect {
                    endpoint: Endpoint::Unix(PathBuf::from("/tmp/admin.sock")),
                    background: false,
                })
            );
        }
        assert!(matches!(
            parse(args(&["--url=web://127.0.0.1:8090"]), None),
            Ok(Options::Connect {
                endpoint: Endpoint::Web(_),
                background: false,
            })
        ));
    }

    #[test]
    fn accepts_help_and_rejects_bad_arguments() {
        assert_eq!(parse(args(&["--help"]), None), Ok(Options::Help));
        assert!(parse(args(&["--url"]), None).is_err());
        assert!(parse(args(&["--unknown"]), None).is_err());
    }

    #[test]
    fn applies_endpoint_environment_before_builtin_default() {
        assert!(matches!(
            parse(
                args(&[]),
                Some(OsString::from("web://127.0.0.1:8091"))
            ),
            Ok(Options::Connect {
                endpoint: Endpoint::Web(url),
                background: false,
            })
                if url.as_str() == "ws://127.0.0.1:8091/api/ws"
        ));
    }

    #[test]
    fn cli_url_overrides_endpoint_environment() {
        assert!(matches!(
            parse(
                args(&["--url", "web://127.0.0.1:8092"]),
                Some(OsString::from("web://127.0.0.1:8091"))
            ),
            Ok(Options::Connect {
                endpoint: Endpoint::Web(url),
                background: false,
            })
                if url.as_str() == "ws://127.0.0.1:8092/api/ws"
        ));
    }

    #[test]
    fn accepts_background_mode() {
        assert!(matches!(
            parse(args(&["--background"]), None),
            Ok(Options::Connect {
                background: true,
                ..
            })
        ));
    }
}
