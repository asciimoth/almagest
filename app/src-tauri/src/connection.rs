use std::sync::Mutex;

use tungstenite::{WebSocket, stream::MaybeTlsStream};

use crate::endpoint::Endpoint;

/// Owns the open admin transport for the lifetime of the Tauri application.
/// Protocol messages will use this state in a later change.
pub(crate) struct AdminConnection {
    _transport: Mutex<Transport>,
}

enum Transport {
    #[cfg(unix)]
    Unix {
        _stream: std::os::unix::net::UnixStream,
    },
    #[cfg(windows)]
    Pipe { _stream: std::fs::File },
    Web {
        _socket: Box<WebSocket<MaybeTlsStream<std::net::TcpStream>>>,
    },
}

impl AdminConnection {
    pub(crate) fn connect(endpoint: &Endpoint) -> Result<Self, String> {
        let transport = match endpoint {
            #[cfg(unix)]
            Endpoint::Unix(path) => std::os::unix::net::UnixStream::connect(path)
                .map(|stream| Transport::Unix { _stream: stream })
                .map_err(|error| format!("connect to Unix socket {}: {error}", path.display()))?,
            #[cfg(not(unix))]
            Endpoint::Unix(path) => {
                return Err(format!(
                    "Unix sockets are not supported on this platform: {}",
                    path.display()
                ));
            }
            #[cfg(windows)]
            Endpoint::Pipe { trust, name } => almagest_windows_pipe::connect(trust.as_str(), name)
                .map(|stream| Transport::Pipe { _stream: stream })
                .map_err(|error| format!("connect to trusted {} pipe: {error}", trust.as_str()))?,
            #[cfg(not(windows))]
            Endpoint::Pipe { .. } => {
                return Err("Windows named pipes are not supported on this platform".to_owned());
            }
            Endpoint::Web(url) => tungstenite::connect(url.as_str())
                .map(|(socket, _response)| Transport::Web {
                    _socket: Box::new(socket),
                })
                .map_err(|error| format!("connect to WebSocket {url}: {error}"))?,
        };
        Ok(Self {
            _transport: Mutex::new(transport),
        })
    }
}

#[cfg(all(test, unix))]
mod tests {
    use super::AdminConnection;
    use crate::endpoint::Endpoint;
    use std::{
        os::unix::net::UnixListener,
        path::PathBuf,
        sync::mpsc,
        thread,
        time::{Duration, SystemTime},
    };

    struct SocketPath(PathBuf);

    impl SocketPath {
        fn unique() -> Self {
            let suffix = SystemTime::now()
                .duration_since(SystemTime::UNIX_EPOCH)
                .expect("system clock must be after the Unix epoch")
                .as_nanos();
            Self(std::env::temp_dir().join(format!(
                "almagest-app-test-{}-{suffix}.sock",
                std::process::id()
            )))
        }
    }

    impl Drop for SocketPath {
        fn drop(&mut self) {
            let _result = std::fs::remove_file(&self.0);
        }
    }

    #[test]
    fn connects_to_unix_socket() {
        let path = SocketPath::unique();
        let listener = UnixListener::bind(&path.0).expect("bind Unix socket");
        let (accepted_tx, accepted_rx) = mpsc::channel();
        let server = thread::spawn(move || {
            let result = listener.accept();
            accepted_tx.send(result.is_ok()).expect("report accept");
        });

        let _connection = AdminConnection::connect(&Endpoint::Unix(path.0.clone()))
            .expect("connect to Unix socket");
        assert!(
            accepted_rx
                .recv_timeout(Duration::from_secs(2))
                .expect("server did not accept in time")
        );
        server.join().expect("server thread");
    }

    #[test]
    fn connects_and_completes_websocket_handshake() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind TCP listener");
        let address = listener.local_addr().expect("listener address");
        let (accepted_tx, accepted_rx) = mpsc::channel();
        let server = thread::spawn(move || {
            let (stream, _) = listener.accept().expect("accept TCP connection");
            let accepted = tungstenite::accept(stream).is_ok();
            accepted_tx.send(accepted).expect("report handshake");
        });

        let endpoint = crate::endpoint::parse(&format!("web://{address}"))
            .expect("parse local WebSocket endpoint");
        let _connection = AdminConnection::connect(&endpoint).expect("connect WebSocket");
        assert!(
            accepted_rx
                .recv_timeout(Duration::from_secs(2))
                .expect("server did not complete the handshake in time")
        );
        server.join().expect("server thread");
    }
}
