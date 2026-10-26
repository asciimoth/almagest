use std::ffi::OsString;

use crate::endpoint::{self, Endpoint};

pub(crate) const HELP: &str = "\
Usage:
  almagest-app [--url ENDPOINT]

Endpoint URLs:
  unix://run/almagest/admin.sock   Unix socket at /run/almagest/admin.sock (default)
  unix:///absolute/path.sock       Unix socket at an explicit absolute path
  pipe://user                      trusted current-user pipe (Windows default)
  pipe://machine                   trusted machine administration pipe
  web://127.0.0.1:8090             WebSocket API at ws://127.0.0.1:8090/api/ws

Options:
  -u, --url ENDPOINT   admin API endpoint URL
  -h, --help           show help
";

#[derive(Debug, Eq, PartialEq)]
pub(crate) enum Options {
    Connect(Endpoint),
    Help,
}

/// Parse only the global endpoint flags supported by `almagest-cli`.
pub(crate) fn parse(args: impl IntoIterator<Item = OsString>) -> Result<Options, String> {
    let mut args = args.into_iter();
    let mut raw_endpoint = None;
    while let Some(argument) = args.next() {
        let argument = argument
            .into_string()
            .map_err(|_| "command-line arguments must be valid UTF-8".to_owned())?;
        match argument.as_str() {
            "-h" | "--help" => return Ok(Options::Help),
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
    endpoint::parse(raw_endpoint.as_deref().unwrap_or_default()).map(Options::Connect)
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
                parse(args(&[flag, "unix:///tmp/admin.sock"])),
                Ok(Options::Connect(Endpoint::Unix(PathBuf::from(
                    "/tmp/admin.sock"
                ))))
            );
        }
        assert!(matches!(
            parse(args(&["--url=web://127.0.0.1:8090"])),
            Ok(Options::Connect(Endpoint::Web(_)))
        ));
    }

    #[test]
    fn accepts_help_and_rejects_bad_arguments() {
        assert_eq!(parse(args(&["--help"])), Ok(Options::Help));
        assert!(parse(args(&["--url"])).is_err());
        assert!(parse(args(&["--unknown"])).is_err());
    }
}
