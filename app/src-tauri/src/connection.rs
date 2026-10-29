use std::{
    io::{BufRead, BufReader, ErrorKind, Read, Write},
    sync::{Arc, Mutex, mpsc},
    thread,
    time::{Duration, Instant},
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
#[cfg(windows)]
const PIPE_POLL_INTERVAL: Duration = Duration::from_millis(10);

/// Match the panel's retry policy. A foreground window retries quickly. A
/// background window reduces wakeups while the daemon is intentionally absent.
const RETRY_POLICY: RetryPolicy = RetryPolicy {
    connect_timeout: Duration::from_secs(3),
    initial_delay: Duration::from_secs(1),
    maximum_delay: Duration::from_secs(30),
};

#[derive(Clone, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub(crate) enum FrontendEvent {
    ConnectionStatus { connected: bool },
    ConnectionError { message: String },
    Message { message: String },
}

/// Owns the admin transport and its retry supervisor. The frontend can only
/// send and receive validated API JSON. Endpoint, retry, and transport details
/// stay in native code.
pub(crate) struct AdminConnection {
    outgoing: Arc<Mutex<Option<ActiveOutgoing>>>,
    frontend: Arc<Frontend>,
    supervisor: mpsc::Sender<SupervisorEvent>,
}

struct ActiveOutgoing {
    id: u64,
    transport: Outgoing,
}

enum Outgoing {
    #[cfg(unix)]
    Unix(Mutex<std::os::unix::net::UnixStream>),
    #[cfg(windows)]
    Pipe(mpsc::SyncSender<String>),
    Web(mpsc::SyncSender<String>),
}

enum PendingConnection {
    #[cfg(unix)]
    Unix {
        reader: std::os::unix::net::UnixStream,
        writer: std::os::unix::net::UnixStream,
    },
    #[cfg(windows)]
    Pipe(std::fs::File),
    Web(Box<WebSocket<MaybeTlsStream<std::net::TcpStream>>>),
}

enum SupervisorEvent {
    AttemptFinished {
        id: u64,
        result: Result<PendingConnection, String>,
    },
    Message {
        id: u64,
        message: String,
    },
    TransportClosed(u64),
    Interactive(bool),
}

struct Frontend {
    state: Mutex<FrontendState>,
}

struct FrontendState {
    channel: Option<Channel<FrontendEvent>>,
    connected: bool,
    status_handler: Option<Arc<dyn Fn(bool) + Send + Sync>>,
}

#[derive(Clone, Copy)]
struct RetryPolicy {
    connect_timeout: Duration,
    initial_delay: Duration,
    maximum_delay: Duration,
}

impl RetryPolicy {
    fn delay(self, attempt: u32, interactive: bool) -> Duration {
        if interactive {
            return self.initial_delay;
        }
        let multiplier = 1_u32.checked_shl(attempt.min(31)).unwrap_or(u32::MAX);
        self.initial_delay
            .saturating_mul(multiplier)
            .min(self.maximum_delay)
    }
}

impl Frontend {
    fn new() -> Self {
        Self {
            state: Mutex::new(FrontendState {
                channel: None,
                connected: false,
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
        crate::logging::write(format_args!(
            "panel admin IPC: register listener (native connected={})",
            state.connected
        ));
        channel
            .send(FrontendEvent::ConnectionStatus {
                connected: state.connected,
            })
            .map_err(|error| format!("send initial connection status: {error}"))?;
        crate::logging::write(format_args!(
            "panel admin IPC: initial connection status queued"
        ));
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

    fn connection_error(&self, message: String) {
        self.send(FrontendEvent::ConnectionError { message });
    }

    fn connected(&self) {
        self.set_connected(true);
    }

    fn disconnected(&self) {
        self.set_connected(false);
    }

    fn set_connected(&self, connected: bool) {
        let handler = if let Ok(mut state) = self.state.lock() {
            if state.connected == connected {
                return;
            }
            state.connected = connected;
            Self::send_locked(&mut state, FrontendEvent::ConnectionStatus { connected });
            state.status_handler.clone()
        } else {
            None
        };
        if let Some(handler) = handler {
            handler(connected);
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
    /// Start the retry supervisor. Initial connection failures are reported as
    /// disconnected state and do not prevent the desktop application startup.
    pub(crate) fn connect(endpoint: &Endpoint) -> Self {
        Self::connect_with_policy(endpoint, RETRY_POLICY)
    }

    fn connect_with_policy(endpoint: &Endpoint, policy: RetryPolicy) -> Self {
        let frontend = Arc::new(Frontend::new());
        let outgoing = Arc::new(Mutex::new(None));
        let (supervisor, events) = mpsc::channel();
        spawn_supervisor(
            endpoint.clone(),
            Arc::clone(&outgoing),
            Arc::clone(&frontend),
            events,
            supervisor.clone(),
            policy,
        );
        Self {
            outgoing,
            frontend,
            supervisor,
        }
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

    /// Select the retry rate from trusted Tauri window state. This setting is
    /// not exposed through frontend IPC.
    pub(crate) fn set_interactive(&self, interactive: bool) {
        let _ = self
            .supervisor
            .send(SupervisorEvent::Interactive(interactive));
    }

    pub(crate) fn send(&self, message: String) -> Result<(), String> {
        validate_request(&message)?;
        if !self.frontend.is_connected()? {
            return Err("admin API is disconnected".to_owned());
        }
        let (id, result) = {
            let active = self
                .outgoing
                .lock()
                .map_err(|_| "admin API connection lock is poisoned")?;
            let active = active
                .as_ref()
                .ok_or_else(|| "admin API is disconnected".to_owned())?;
            let result = match &active.transport {
                #[cfg(unix)]
                Outgoing::Unix(stream) => write_stream(stream, &message),
                #[cfg(windows)]
                Outgoing::Pipe(sender) => sender
                    .try_send(message)
                    .map_err(|error| format!("queue admin API pipe message: {error}")),
                Outgoing::Web(sender) => sender
                    .try_send(message)
                    .map_err(|error| format!("queue admin API message: {error}")),
            };
            (active.id, result)
        };
        if result.is_err() {
            let _ = self.supervisor.send(SupervisorEvent::TransportClosed(id));
        }
        result
    }
}

// Keep the state-machine transitions in one function so timeout, stale-result,
// and active-transport rules can be reviewed together.
#[allow(clippy::too_many_lines)]
fn spawn_supervisor(
    endpoint: Endpoint,
    outgoing: Arc<Mutex<Option<ActiveOutgoing>>>,
    frontend: Arc<Frontend>,
    events: mpsc::Receiver<SupervisorEvent>,
    sender: mpsc::Sender<SupervisorEvent>,
    policy: RetryPolicy,
) {
    thread::spawn(move || {
        let mut interactive = true;
        let mut retry_attempt = 0;
        let mut next_attempt = Instant::now();
        let mut attempt: Option<(u64, Instant)> = None;
        let mut active = None;
        let mut next_id = 1_u64;

        loop {
            let deadline = attempt.map_or(next_attempt, |(_, deadline)| deadline);
            let event = if active.is_some() {
                events
                    .recv()
                    .map_err(|_| mpsc::RecvTimeoutError::Disconnected)
            } else {
                events.recv_timeout(deadline.saturating_duration_since(Instant::now()))
            };

            match event {
                Ok(SupervisorEvent::Interactive(value)) => {
                    let became_interactive = value && !interactive;
                    interactive = value;
                    if became_interactive && active.is_none() {
                        // A blocked OS connection cannot always be cancelled.
                        // Invalidate it so its late result is ignored, then let
                        // the next loop start a new foreground attempt.
                        attempt = None;
                        retry_attempt = 0;
                        next_attempt = Instant::now();
                    }
                }
                Ok(SupervisorEvent::AttemptFinished { id, result }) => {
                    if attempt.is_none_or(|(current, _)| current != id) {
                        continue;
                    }
                    attempt = None;
                    match result {
                        Ok(connection) => {
                            let transport =
                                match activate_connection(connection, id, sender.clone()) {
                                    Ok(transport) => transport,
                                    Err(error) => {
                                        let message = format!("connect to admin API: {error}");
                                        crate::logging::write(format_args!("{message}"));
                                        frontend.connection_error(message);
                                        schedule_retry(
                                            &mut next_attempt,
                                            &mut retry_attempt,
                                            interactive,
                                            policy,
                                        );
                                        continue;
                                    }
                                };
                            if let Ok(mut slot) = outgoing.lock() {
                                *slot = Some(ActiveOutgoing { id, transport });
                                active = Some(id);
                                retry_attempt = 0;
                                crate::logging::write(format_args!(
                                    "admin API connected transport_id={id}"
                                ));
                                frontend.connected();
                            } else {
                                break;
                            }
                        }
                        Err(error) => {
                            let message = format!("connect to admin API: {error}");
                            crate::logging::write(format_args!("{message}"));
                            frontend.connection_error(message);
                            schedule_retry(
                                &mut next_attempt,
                                &mut retry_attempt,
                                interactive,
                                policy,
                            );
                        }
                    }
                }
                Ok(SupervisorEvent::Message { id, message }) => {
                    if active == Some(id) {
                        frontend.message(message);
                    }
                }
                Ok(SupervisorEvent::TransportClosed(id)) => {
                    if active != Some(id) {
                        continue;
                    }
                    if let Ok(mut slot) = outgoing.lock() {
                        if slot.as_ref().is_some_and(|current| current.id == id) {
                            *slot = None;
                        }
                    } else {
                        break;
                    }
                    active = None;
                    crate::logging::write(format_args!("admin API disconnected transport_id={id}"));
                    frontend.disconnected();
                    schedule_retry(&mut next_attempt, &mut retry_attempt, interactive, policy);
                }
                Err(mpsc::RecvTimeoutError::Timeout) => {
                    if attempt.take().is_some() {
                        let message = "connect to admin API: attempt timed out".to_owned();
                        crate::logging::write(format_args!("{message}"));
                        frontend.connection_error(message);
                        schedule_retry(&mut next_attempt, &mut retry_attempt, interactive, policy);
                    } else {
                        let id = next_id;
                        next_id = next_id.wrapping_add(1);
                        spawn_connection_attempt(endpoint.clone(), id, sender.clone());
                        attempt = Some((id, Instant::now() + policy.connect_timeout));
                    }
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
            }
        }
    });
}

fn schedule_retry(
    next_attempt: &mut Instant,
    retry_attempt: &mut u32,
    interactive: bool,
    policy: RetryPolicy,
) {
    *next_attempt = Instant::now() + policy.delay(*retry_attempt, interactive);
    *retry_attempt = retry_attempt.saturating_add(1);
}

fn spawn_connection_attempt(endpoint: Endpoint, id: u64, sender: mpsc::Sender<SupervisorEvent>) {
    thread::spawn(move || {
        let result = connect_endpoint(&endpoint);
        let _ = sender.send(SupervisorEvent::AttemptFinished { id, result });
    });
}

fn connect_endpoint(endpoint: &Endpoint) -> Result<PendingConnection, String> {
    match endpoint {
        #[cfg(unix)]
        Endpoint::Unix(path) => {
            let reader = std::os::unix::net::UnixStream::connect(path)
                .map_err(|error| format!("connect to Unix socket {}: {error}", path.display()))?;
            let writer = reader
                .try_clone()
                .map_err(|error| format!("clone Unix socket: {error}"))?;
            Ok(PendingConnection::Unix { reader, writer })
        }
        #[cfg(not(unix))]
        Endpoint::Unix(path) => Err(format!(
            "Unix sockets are not supported on this platform: {}",
            path.display()
        )),
        #[cfg(windows)]
        Endpoint::Pipe { trust, name } => {
            let stream = almagest_windows_pipe::connect(trust.as_str(), name)
                .map_err(|error| format!("connect to trusted {} pipe: {error}", trust.as_str()))?;
            Ok(PendingConnection::Pipe(stream))
        }
        #[cfg(not(windows))]
        Endpoint::Pipe { .. } => {
            Err("Windows named pipes are not supported on this platform".to_owned())
        }
        Endpoint::Web(url) => {
            let (socket, _response) = tungstenite::connect(url.as_str())
                .map_err(|error| format!("connect to WebSocket {url}: {error}"))?;
            Ok(PendingConnection::Web(Box::new(socket)))
        }
    }
}

fn activate_connection(
    connection: PendingConnection,
    id: u64,
    supervisor: mpsc::Sender<SupervisorEvent>,
) -> Result<Outgoing, String> {
    match connection {
        #[cfg(unix)]
        PendingConnection::Unix { reader, writer } => {
            spawn_stream_reader(reader, id, supervisor);
            Ok(Outgoing::Unix(Mutex::new(writer)))
        }
        #[cfg(windows)]
        PendingConnection::Pipe(stream) => {
            let (sender, receiver) = mpsc::sync_channel(64);
            spawn_pipe_worker(stream, receiver, id, supervisor);
            Ok(Outgoing::Pipe(sender))
        }
        PendingConnection::Web(mut socket) => {
            // The handshake is complete before nonblocking polling starts.
            set_web_nonblocking(&mut socket)?;
            let (sender, receiver) = mpsc::sync_channel(64);
            spawn_web_worker(*socket, receiver, id, supervisor);
            Ok(Outgoing::Web(sender))
        }
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

#[cfg(windows)]
fn spawn_pipe_worker(
    mut stream: std::fs::File,
    outgoing: mpsc::Receiver<String>,
    id: u64,
    supervisor: mpsc::Sender<SupervisorEvent>,
) {
    thread::spawn(move || {
        let mut incoming = Vec::new();
        let mut sent_first_request = false;
        let mut received_first_response = false;
        crate::logging::write(format_args!(
            "admin API pipe worker started transport_id={id}"
        ));
        'connection: loop {
            match outgoing.recv_timeout(PIPE_POLL_INTERVAL) {
                Ok(message) => {
                    if let Err(error) = stream
                        .write_all(message.as_bytes())
                        .and_then(|()| stream.write_all(b"\n"))
                        .and_then(|()| stream.flush())
                    {
                        crate::logging::write(format_args!(
                            "admin API pipe write failed transport_id={id}: {error}"
                        ));
                        break;
                    }
                    if !sent_first_request {
                        sent_first_request = true;
                        crate::logging::write(format_args!(
                            "admin API pipe first request written transport_id={id}"
                        ));
                    }
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
                Err(mpsc::RecvTimeoutError::Timeout) => {}
            }

            loop {
                let available = match almagest_windows_pipe::available_bytes(&stream) {
                    Ok(0) => break,
                    Ok(available) => available,
                    Err(error) => {
                        crate::logging::write(format_args!(
                            "admin API pipe poll failed transport_id={id}: {error}"
                        ));
                        break 'connection;
                    }
                };
                let remaining = MAX_MESSAGE_BYTES
                    .saturating_add(1)
                    .saturating_sub(incoming.len());
                if remaining == 0 {
                    break 'connection;
                }
                let count = available.min(remaining);
                let offset = incoming.len();
                incoming.resize(offset + count, 0);
                match stream.read(&mut incoming[offset..]) {
                    Ok(0) => break 'connection,
                    Err(error) => {
                        crate::logging::write(format_args!(
                            "admin API pipe read failed transport_id={id}: {error}"
                        ));
                        break 'connection;
                    }
                    Ok(read) => incoming.truncate(offset + read),
                }

                while let Some(newline) = incoming.iter().position(|byte| *byte == b'\n') {
                    let mut line = incoming.drain(..=newline).collect::<Vec<_>>();
                    while line
                        .last()
                        .is_some_and(|byte| matches!(byte, b'\n' | b'\r'))
                    {
                        line.pop();
                    }
                    let Ok(message) = String::from_utf8(line) else {
                        crate::logging::write(format_args!(
                            "admin API pipe returned invalid UTF-8 transport_id={id}"
                        ));
                        break 'connection;
                    };
                    if !received_first_response {
                        received_first_response = true;
                        crate::logging::write(format_args!(
                            "admin API pipe first response received transport_id={id}"
                        ));
                    }
                    if supervisor
                        .send(SupervisorEvent::Message { id, message })
                        .is_err()
                    {
                        return;
                    }
                }
                if incoming.len() > MAX_MESSAGE_BYTES {
                    crate::logging::write(format_args!(
                        "admin API pipe message is too large transport_id={id}"
                    ));
                    break 'connection;
                }
            }
        }
        let _ = supervisor.send(SupervisorEvent::TransportClosed(id));
    });
}

fn spawn_stream_reader<R: Read + Send + 'static>(
    stream: R,
    id: u64,
    supervisor: mpsc::Sender<SupervisorEvent>,
) {
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
                    if supervisor
                        .send(SupervisorEvent::Message { id, message })
                        .is_err()
                    {
                        return;
                    }
                }
            }
        }
        let _ = supervisor.send(SupervisorEvent::TransportClosed(id));
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
    id: u64,
    supervisor: mpsc::Sender<SupervisorEvent>,
) {
    thread::spawn(move || {
        'connection: loop {
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
                    Ok(Message::Text(message)) => {
                        if supervisor
                            .send(SupervisorEvent::Message {
                                id,
                                message: message.to_string(),
                            })
                            .is_err()
                        {
                            return;
                        }
                    }
                    Ok(Message::Close(_)) => break 'connection,
                    Ok(_) => {}
                    Err(tungstenite::Error::Io(error))
                        if matches!(error.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut) =>
                    {
                        break;
                    }
                    Err(_) => break 'connection,
                }
            }
            if let Err(error) = socket.flush()
                && !is_transient_web_error(&error)
            {
                break;
            }
        }
        let _ = supervisor.send(SupervisorEvent::TransportClosed(id));
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
    use super::{AdminConnection, Frontend, FrontendEvent, RetryPolicy, validate_request};
    use crate::endpoint::Endpoint;
    use std::{
        io::{BufRead, BufReader},
        sync::mpsc,
        thread,
        time::Duration,
    };
    use tungstenite::Message;

    const REQUEST: &str = r#"{"version":"v1","kind":"request","id":"1","surface":"admin","method":"ping","payload":{}}"#;
    const TEST_POLICY: RetryPolicy = RetryPolicy {
        connect_timeout: Duration::from_millis(100),
        initial_delay: Duration::from_millis(10),
        maximum_delay: Duration::from_millis(40),
    };

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
    fn retry_delay_matches_panel_policy() {
        let policy = RetryPolicy {
            connect_timeout: Duration::ZERO,
            initial_delay: Duration::from_secs(1),
            maximum_delay: Duration::from_secs(30),
        };
        for attempt in 0..8 {
            assert_eq!(policy.delay(attempt, true), Duration::from_secs(1));
        }
        assert_eq!(
            (0..7)
                .map(|attempt| policy.delay(attempt, false))
                .collect::<Vec<_>>(),
            [1, 2, 4, 8, 16, 30, 30].map(Duration::from_secs)
        );
    }

    #[test]
    fn reports_connection_status_to_rust_handler() {
        let frontend = Frontend::new();
        let (sender, receiver) = mpsc::channel();
        frontend
            .set_status_handler(move |connected| sender.send(connected).expect("report status"))
            .expect("set status handler");

        assert!(!receiver.recv().expect("initial status"));
        frontend.connected();
        assert!(receiver.recv().expect("connected status"));
        frontend.disconnected();
        assert!(!receiver.recv().expect("disconnected status"));
        frontend.disconnected();
        assert!(receiver.try_recv().is_err());
    }

    #[test]
    fn serializes_frontend_connection_errors_for_the_panel() {
        let event = FrontendEvent::ConnectionError {
            message: "connect failed".to_owned(),
        };
        assert_eq!(
            serde_json::to_value(event).expect("serialize frontend event"),
            serde_json::json!({
                "type": "connection_error",
                "message": "connect failed",
            })
        );
    }

    #[cfg(unix)]
    #[test]
    fn reconnects_when_unix_socket_appears_after_startup() {
        let path = temporary_socket_path();
        let connection =
            AdminConnection::connect_with_policy(&Endpoint::Unix(path.clone()), TEST_POLICY);
        let (status_sender, status_receiver) = mpsc::channel();
        connection
            .set_status_handler(move |connected| {
                status_sender.send(connected).expect("report status");
            })
            .expect("set status handler");
        assert!(!status_receiver.recv().expect("initial status"));

        // The first attempt fails because the listener does not exist yet.
        thread::sleep(Duration::from_millis(30));
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

        assert!(
            status_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("app did not reconnect")
        );
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

    #[cfg(unix)]
    #[test]
    fn reconnects_after_unix_transport_closes() {
        let path = temporary_socket_path();
        let first_listener =
            std::os::unix::net::UnixListener::bind(&path).expect("bind first Unix socket");
        let (close_sender, close_receiver) = mpsc::channel();
        let first_server = thread::spawn(move || {
            let (stream, _) = first_listener.accept().expect("accept first connection");
            close_receiver.recv().expect("wait for close request");
            drop(stream);
        });

        let connection =
            AdminConnection::connect_with_policy(&Endpoint::Unix(path.clone()), TEST_POLICY);
        let (status_sender, status_receiver) = mpsc::channel();
        connection
            .set_status_handler(move |connected| {
                status_sender.send(connected).expect("report status");
            })
            .expect("set status handler");
        assert!(!status_receiver.recv().expect("initial status"));
        assert!(
            status_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("app did not make the first connection")
        );

        close_sender.send(()).expect("request first close");
        first_server.join().expect("first server thread");
        assert!(
            !status_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("app did not detect the closed transport")
        );
        std::fs::remove_file(&path).expect("remove first Unix socket");

        let second_listener =
            std::os::unix::net::UnixListener::bind(&path).expect("bind second Unix socket");
        let (message_sender, message_receiver) = mpsc::channel();
        let second_server = thread::spawn(move || {
            let (stream, _) = second_listener.accept().expect("accept second connection");
            let mut message = String::new();
            BufReader::new(stream)
                .read_line(&mut message)
                .expect("read request");
            message_sender.send(message).expect("report request");
        });

        assert!(
            status_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("app did not reconnect after transport loss")
        );
        connection.send(REQUEST.to_owned()).expect("send request");
        assert_eq!(
            message_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("second server did not receive request"),
            format!("{REQUEST}\n")
        );
        second_server.join().expect("second server thread");
        std::fs::remove_file(path).expect("remove second Unix socket");
    }

    #[test]
    fn retries_after_websocket_handshake_timeout() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind TCP listener");
        let address = listener.local_addr().expect("listener address");
        let (accepted_sender, accepted_receiver) = mpsc::channel();
        let server = thread::spawn(move || {
            // Keep both TCP connections open without completing either
            // WebSocket handshake. The second accept proves that the timed-out
            // first attempt did not stop the retry supervisor.
            let (first, _) = listener.accept().expect("accept first TCP connection");
            accepted_sender.send(()).expect("report first connection");
            let (second, _) = listener.accept().expect("accept second TCP connection");
            accepted_sender.send(()).expect("report second connection");
            drop((first, second));
        });
        let Endpoint::Web(endpoint) =
            crate::endpoint::parse(&format!("web://{address}")).expect("parse WebSocket endpoint")
        else {
            panic!("wrong endpoint kind");
        };

        let _connection =
            AdminConnection::connect_with_policy(&Endpoint::Web(endpoint), TEST_POLICY);
        accepted_receiver
            .recv_timeout(Duration::from_secs(2))
            .expect("app did not make the first connection attempt");
        accepted_receiver
            .recv_timeout(Duration::from_secs(2))
            .expect("app did not retry the timed-out connection attempt");
        server.join().expect("server thread");
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
            AdminConnection::connect_with_policy(&Endpoint::Web(endpoint), TEST_POLICY);
        wait_until_connected(&connection);
        connection.send(REQUEST.to_owned()).expect("send request");
        assert_eq!(
            message_receiver
                .recv_timeout(Duration::from_secs(2))
                .expect("server did not receive request"),
            Message::Text(REQUEST.into())
        );
        server.join().expect("server thread");
    }

    fn wait_until_connected(connection: &AdminConnection) {
        let (sender, receiver) = mpsc::channel();
        connection
            .set_status_handler(move |connected| sender.send(connected).expect("report status"))
            .expect("set status handler");
        if !receiver.recv().expect("initial status") {
            assert!(
                receiver
                    .recv_timeout(Duration::from_secs(2))
                    .expect("app did not connect")
            );
        }
    }

    #[cfg(unix)]
    fn temporary_socket_path() -> std::path::PathBuf {
        std::env::temp_dir().join(format!(
            "almagest-app-test-{}-{}.sock",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::SystemTime::UNIX_EPOCH)
                .expect("system clock must be after Unix epoch")
                .as_nanos()
        ))
    }
}
