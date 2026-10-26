use std::{
    io::{BufRead, BufReader, ErrorKind, Read, Write},
    sync::{Arc, Mutex, mpsc},
    thread,
    time::Duration,
};

use serde::Serialize;
use tauri::ipc::Channel;
use tungstenite::{Message, WebSocket, stream::MaybeTlsStream};

use crate::endpoint::Endpoint;

// Keep enough room for the daemon's maximum 32 MiB snapshot after base64
// encoding, plus the API envelope allowance.
const MAX_MESSAGE_BYTES: usize = (32_usize << 20).div_ceil(3) * 4 + (1 << 20);
const MAX_MESSAGE_READ_BYTES: u64 = (32_u64 << 20).div_ceil(3) * 4 + (1 << 20) + 1;
const WEB_POLL_INTERVAL: Duration = Duration::from_millis(10);

#[derive(Clone, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub(crate) enum FrontendEvent {
    ConnectionStatus { connected: bool },
    Message { message: String },
}

/// Owns the admin transport. The frontend can only send and receive API JSON;
/// endpoint and transport details stay in this module.
pub(crate) struct AdminConnection {
    outgoing: Outgoing,
    frontend: Arc<Frontend>,
}

enum Outgoing {
    #[cfg(unix)]
    Unix(Mutex<std::os::unix::net::UnixStream>),
    #[cfg(windows)]
    Pipe(Mutex<std::fs::File>),
    Web(mpsc::SyncSender<String>),
}

struct Frontend {
    state: Mutex<FrontendState>,
}

struct FrontendState {
    channel: Option<Channel<FrontendEvent>>,
    connected: bool,
    status_handler: Option<Arc<dyn Fn(bool) + Send + Sync>>,
}

impl Frontend {
    fn new() -> Self {
        Self {
            state: Mutex::new(FrontendState {
                channel: None,
                connected: true,
                status_handler: None,
            }),
        }
    }

    fn set_status_handler(
        &self,
        handler: impl Fn(bool) + Send + Sync + 'static,
    ) -> Result<(), String> {
        let handler = Arc::new(handler);
        let connected = {
            let mut state = self
                .state
                .lock()
                .map_err(|_| "frontend state lock is poisoned")?;
            state.status_handler = Some(handler.clone());
            state.connected
        };
        handler(connected);
        Ok(())
    }

    fn register(&self, channel: Channel<FrontendEvent>) -> Result<(), String> {
        let mut state = self
            .state
            .lock()
            .map_err(|_| "frontend state lock is poisoned")?;
        channel
            .send(FrontendEvent::ConnectionStatus {
                connected: state.connected,
            })
            .map_err(|error| format!("send initial connection status: {error}"))?;
        state.channel = Some(channel);
        Ok(())
    }

    fn is_connected(&self) -> Result<bool, String> {
        self.state
            .lock()
            .map(|state| state.connected)
            .map_err(|_| "frontend state lock is poisoned".to_owned())
    }

    fn message(&self, message: String) {
        self.send(FrontendEvent::Message { message });
    }

    fn disconnected(&self) {
        let handler = if let Ok(mut state) = self.state.lock() {
            if !state.connected {
                return;
            }
            state.connected = false;
            Self::send_locked(
                &mut state,
                FrontendEvent::ConnectionStatus { connected: false },
            );
            state.status_handler.clone()
        } else {
            None
        };
        if let Some(handler) = handler {
            handler(false);
        }
    }

    fn send(&self, event: FrontendEvent) {
        if let Ok(mut state) = self.state.lock() {
            Self::send_locked(&mut state, event);
        }
    }

    fn send_locked(state: &mut FrontendState, event: FrontendEvent) {
        if state
            .channel
            .as_ref()
            .is_some_and(|channel| channel.send(event).is_err())
        {
            state.channel = None;
        }
    }
}

impl AdminConnection {
    pub(crate) fn connect(endpoint: &Endpoint) -> Result<Self, String> {
        let frontend = Arc::new(Frontend::new());
        let outgoing = match endpoint {
            #[cfg(unix)]
            Endpoint::Unix(path) => {
                let stream = std::os::unix::net::UnixStream::connect(path).map_err(|error| {
                    format!("connect to Unix socket {}: {error}", path.display())
                })?;
                let writer = stream
                    .try_clone()
                    .map_err(|error| format!("clone Unix socket: {error}"))?;
                spawn_stream_reader(stream, Arc::clone(&frontend));
                Outgoing::Unix(Mutex::new(writer))
            }
            #[cfg(not(unix))]
            Endpoint::Unix(path) => {
                return Err(format!(
                    "Unix sockets are not supported on this platform: {}",
                    path.display()
                ));
            }
            #[cfg(windows)]
            Endpoint::Pipe { trust, name } => {
                let stream =
                    almagest_windows_pipe::connect(trust.as_str(), name).map_err(|error| {
                        format!("connect to trusted {} pipe: {error}", trust.as_str())
                    })?;
                let writer = stream
                    .try_clone()
                    .map_err(|error| format!("clone Windows pipe: {error}"))?;
                spawn_stream_reader(stream, Arc::clone(&frontend));
                Outgoing::Pipe(Mutex::new(writer))
            }
            #[cfg(not(windows))]
            Endpoint::Pipe { .. } => {
                return Err("Windows named pipes are not supported on this platform".to_owned());
            }
            Endpoint::Web(url) => {
                let (mut socket, _response) = tungstenite::connect(url.as_str())
                    .map_err(|error| format!("connect to WebSocket {url}: {error}"))?;
                set_web_nonblocking(&mut socket)?;
                let (sender, receiver) = mpsc::sync_channel(64);
                spawn_web_worker(socket, receiver, Arc::clone(&frontend));
                Outgoing::Web(sender)
            }
        };
        Ok(Self { outgoing, frontend })
    }

    pub(crate) fn register(&self, channel: Channel<FrontendEvent>) -> Result<(), String> {
        self.frontend.register(channel)
    }

    pub(crate) fn set_status_handler(
        &self,
        handler: impl Fn(bool) + Send + Sync + 'static,
    ) -> Result<(), String> {
        self.frontend.set_status_handler(handler)
    }

    pub(crate) fn send(&self, message: String) -> Result<(), String> {
        validate_request(&message)?;
        if !self.frontend.is_connected()? {
            return Err("admin API is disconnected".to_owned());
        }
        let result = match &self.outgoing {
            #[cfg(unix)]
            Outgoing::Unix(stream) => write_stream(stream, &message),
            #[cfg(windows)]
            Outgoing::Pipe(stream) => write_stream(stream, &message),
            Outgoing::Web(sender) => sender
                .try_send(message)
                .map_err(|error| format!("queue admin API message: {error}")),
        };
        if result.is_err() {
            self.frontend.disconnected();
        }
        result
    }
}

fn validate_request(message: &str) -> Result<(), String> {
    if message.len() > MAX_MESSAGE_BYTES {
        return Err("admin API message is too large".to_owned());
    }
    let value: serde_json::Value = serde_json::from_str(message)
        .map_err(|error| format!("invalid admin API JSON: {error}"))?;
    let object = value
        .as_object()
        .ok_or_else(|| "admin API message must be an object".to_owned())?;
    if object.get("version").and_then(serde_json::Value::as_str) != Some("v1")
        || object.get("kind").and_then(serde_json::Value::as_str) != Some("request")
        || !has_nonempty_string(object, "id")
        || !has_nonempty_string(object, "surface")
        || !has_nonempty_string(object, "method")
    {
        return Err(
            "admin API message must be a v1 request with id, surface, and method".to_owned(),
        );
    }
    Ok(())
}

fn has_nonempty_string(object: &serde_json::Map<String, serde_json::Value>, key: &str) -> bool {
    object
        .get(key)
        .and_then(serde_json::Value::as_str)
        .is_some_and(|value| !value.is_empty())
}

fn write_stream<W: Write>(stream: &Mutex<W>, message: &str) -> Result<(), String> {
    let mut stream = stream
        .lock()
        .map_err(|_| "admin API writer lock is poisoned")?;
    stream
        .write_all(message.as_bytes())
        .and_then(|()| stream.write_all(b"\n"))
        .and_then(|()| stream.flush())
        .map_err(|error| format!("write admin API message: {error}"))
}

fn spawn_stream_reader<R: Read + Send + 'static>(stream: R, frontend: Arc<Frontend>) {
    thread::spawn(move || {
        let mut reader = BufReader::new(stream);
        loop {
            let mut message = String::new();
            match reader
                .by_ref()
                .take(MAX_MESSAGE_READ_BYTES)
                .read_line(&mut message)
            {
                Ok(0) | Err(_) => break,
                Ok(_) => {
                    if message.len() > MAX_MESSAGE_BYTES {
                        break;
                    }
                    while message.ends_with(['\n', '\r']) {
                        message.pop();
                    }
                    frontend.message(message);
                }
            }
        }
        frontend.disconnected();
    });
}

fn set_web_nonblocking(
    socket: &mut WebSocket<MaybeTlsStream<std::net::TcpStream>>,
) -> Result<(), String> {
    match socket.get_mut() {
        MaybeTlsStream::Plain(stream) => stream
            .set_nonblocking(true)
            .map_err(|error| format!("configure WebSocket polling: {error}")),
        _ => Err("the app supports only plain local WebSocket endpoints".to_owned()),
    }
}

fn spawn_web_worker(
    mut socket: WebSocket<MaybeTlsStream<std::net::TcpStream>>,
    outgoing: mpsc::Receiver<String>,
    frontend: Arc<Frontend>,
) {
    thread::spawn(move || {
        loop {
            match outgoing.recv_timeout(WEB_POLL_INTERVAL) {
                Ok(message) => {
                    if let Err(error) = socket.send(Message::Text(message.into()))
                        && !is_transient_web_error(&error)
                    {
                        break;
                    }
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
                Err(mpsc::RecvTimeoutError::Timeout) => {}
            }
            loop {
                match socket.read() {
                    Ok(Message::Text(message)) => frontend.message(message.to_string()),
                    Ok(Message::Close(_)) => {
                        frontend.disconnected();
                        return;
                    }
                    Ok(_) => {}
                    Err(tungstenite::Error::Io(error))
                        if matches!(error.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut) =>
                    {
                        break;
                    }
                    Err(_) => {
                        frontend.disconnected();
                        return;
                    }
                }
            }
            if let Err(error) = socket.flush()
                && !is_transient_web_error(&error)
            {
                break;
            }
        }
        frontend.disconnected();
    });
}

fn is_transient_web_error(error: &tungstenite::Error) -> bool {
    matches!(
        error,
        tungstenite::Error::Io(error)
            if matches!(error.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut)
    )
}

#[cfg(test)]
mod tests {
    use super::{AdminConnection, Frontend, validate_request};
    use crate::endpoint::Endpoint;
    use std::{
        io::{BufRead, BufReader},
        sync::mpsc,
        thread,
        time::Duration,
    };
    use tungstenite::Message;

    const REQUEST: &str = r#"{"version":"v1","kind":"request","id":"1","surface":"admin","method":"ping","payload":{}}"#;

    #[test]
    fn accepts_only_api_requests() {
        assert!(validate_request(REQUEST).is_ok());
        for invalid in [
            "not JSON",
            r#"{"version":"v1","kind":"event","surface":"admin","method":"ping"}"#,
            r#"{"version":"v1","kind":"request","id":"","surface":"admin","method":"ping"}"#,
        ] {
            assert!(validate_request(invalid).is_err(), "accepted {invalid}");
        }
    }

    #[test]
    fn reports_connection_status_to_rust_handler() {
        let frontend = Frontend::new();
        let (sender, receiver) = mpsc::channel();
        frontend
            .set_status_handler(move |connected| sender.send(connected).expect("report status"))
            .expect("set status handler");

        assert!(receiver.recv().expect("initial status"));
        frontend.disconnected();
        assert!(!receiver.recv().expect("disconnected status"));
        frontend.disconnected();
        assert!(receiver.try_recv().is_err());
    }

    #[cfg(unix)]
    #[test]
    fn relays_request_to_unix_stream() {
        let directory = std::env::temp_dir();
        let path = directory.join(format!(
            "almagest-app-test-{}-{}.sock",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::SystemTime::UNIX_EPOCH)
                .expect("system clock must be after Unix epoch")
                .as_nanos()
        ));
        let listener = std::os::unix::net::UnixListener::bind(&path).expect("bind Unix socket");
        let (message_sender, message_receiver) = mpsc::channel();
        let server = thread::spawn(move || {
            let (stream, _) = listener.accept().expect("accept Unix connection");
            let mut message = String::new();
            BufReader::new(stream)
                .read_line(&mut message)
                .expect("read request");
            message_sender.send(message).expect("report request");
        });

        let connection =
            AdminConnection::connect(&Endpoint::Unix(path.clone())).expect("connect Unix socket");
        connection.send(REQUEST.to_owned()).expect("send request");
        assert_eq!(
            message_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("server did not receive request"),
            format!("{REQUEST}\n")
        );
        server.join().expect("server thread");
        std::fs::remove_file(path).expect("remove Unix socket");
    }

    #[test]
    fn relays_request_to_websocket() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind TCP listener");
        let address = listener.local_addr().expect("listener address");
        let (message_sender, message_receiver) = mpsc::channel();
        let server = thread::spawn(move || {
            let (stream, _) = listener.accept().expect("accept TCP connection");
            let mut socket = tungstenite::accept(stream).expect("accept WebSocket");
            let message = socket.read().expect("read request");
            message_sender.send(message).expect("report request");
        });

        let Endpoint::Web(endpoint) =
            crate::endpoint::parse(&format!("web://{address}")).expect("parse WebSocket endpoint")
        else {
            panic!("wrong endpoint kind");
        };
        let connection =
            AdminConnection::connect(&Endpoint::Web(endpoint)).expect("connect WebSocket");
        connection.send(REQUEST.to_owned()).expect("send request");
        assert_eq!(
            message_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("server did not receive request"),
            Message::Text(REQUEST.into())
        );
        server.join().expect("server thread");
    }
}
