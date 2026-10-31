use std::{
    fmt,
    fs::{self, File, OpenOptions},
    io::{self, Write},
    path::{Path, PathBuf},
    sync::{Mutex, OnceLock},
    time::{SystemTime, UNIX_EPOCH},
};

const LOG_NAME: &str = "almagest-app.log";
const LOG_MAX_BYTES: u64 = 5 * 1024 * 1024;
const LOG_BACKUPS: usize = 3;

static LOG: OnceLock<Mutex<LogFile>> = OnceLock::new();

struct LogFile {
    path: PathBuf,
    file: Option<File>,
}

impl LogFile {
    fn open(path: PathBuf) -> io::Result<(Self, Option<io::Error>)> {
        let rotation_error = rotate_if_full(&path).err();
        let file = open_file(&path)?;
        Ok((
            Self {
                path,
                file: Some(file),
            },
            rotation_error,
        ))
    }

    fn write(&mut self, timestamp: u128, message: fmt::Arguments<'_>) {
        let rotation_error = self.rotate_if_full();
        let Some(file) = self.file.as_mut() else {
            return;
        };
        if let Some(error) = rotation_error {
            let _ = writeln!(file, "{timestamp} rotate desktop app log: {error}");
        }
        let _ = writeln!(file, "{timestamp} {message}");
        let _ = file.flush();
    }

    fn rotate_if_full(&mut self) -> Option<io::Error> {
        let full = self
            .file
            .as_ref()
            .and_then(|file| file.metadata().ok())
            .is_some_and(|metadata| metadata.len() >= LOG_MAX_BYTES);
        if !full {
            return None;
        }
        self.file = None;
        let rotation_error = rotate_if_full(&self.path).err();
        match open_file(&self.path) {
            Ok(file) => self.file = Some(file),
            Err(error) => return Some(error),
        }
        rotation_error
    }
}

pub(crate) fn initialize() -> io::Result<PathBuf> {
    let directory = log_directory()?;
    fs::create_dir_all(&directory)?;
    let path = directory.join(LOG_NAME);
    let (log, rotation_error) = LogFile::open(path.clone())?;
    LOG.set(Mutex::new(log)).map_err(|_| {
        io::Error::new(
            io::ErrorKind::AlreadyExists,
            "desktop app logging is already initialized",
        )
    })?;
    if let Some(error) = rotation_error {
        write(format_args!("rotate desktop app log: {error}"));
    }
    Ok(path)
}

#[cfg(windows)]
fn log_directory() -> io::Result<PathBuf> {
    let local = std::env::var_os("LOCALAPPDATA")
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "LOCALAPPDATA is not available"))?;
    Ok(PathBuf::from(local).join("Almagest").join("logs"))
}

#[cfg(not(windows))]
fn log_directory() -> io::Result<PathBuf> {
    if let Some(state) = std::env::var_os("XDG_STATE_HOME") {
        return Ok(PathBuf::from(state).join("almagest").join("logs"));
    }
    let home = std::env::var_os("HOME")
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "HOME is not available"))?;
    Ok(PathBuf::from(home)
        .join(".local")
        .join("state")
        .join("almagest")
        .join("logs"))
}

pub(crate) fn write(message: fmt::Arguments<'_>) {
    #[cfg(debug_assertions)]
    eprintln!("{message}");

    let Some(log) = LOG.get() else {
        return;
    };
    let timestamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |duration| duration.as_millis());
    if let Ok(mut log) = log.lock() {
        log.write(timestamp, message);
    }
}

fn open_file(path: &Path) -> io::Result<File> {
    OpenOptions::new().create(true).append(true).open(path)
}

fn rotate_if_full(path: &Path) -> io::Result<()> {
    let metadata = match fs::metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error),
    };
    if metadata.len() < LOG_MAX_BYTES {
        return Ok(());
    }
    let oldest = backup_path(path, LOG_BACKUPS);
    match fs::remove_file(oldest) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error),
    }
    for index in (1..LOG_BACKUPS).rev() {
        let source = backup_path(path, index);
        let destination = backup_path(path, index + 1);
        match fs::rename(source, destination) {
            Ok(()) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    fs::rename(path, backup_path(path, 1))
}

fn backup_path(path: &Path, index: usize) -> PathBuf {
    let mut name = path.as_os_str().to_owned();
    name.push(format!(".{index}"));
    PathBuf::from(name)
}

#[cfg(test)]
mod tests {
    use super::{LOG_BACKUPS, LOG_MAX_BYTES, LogFile, backup_path, rotate_if_full};
    use std::{fs, io::Write as _, time::SystemTime};

    #[test]
    fn rotates_full_log_and_retains_bounded_backups() {
        let unique = SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "almagest-app-log-test-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&directory).expect("create test directory");
        let path = directory.join("app.log");
        fs::write(&path, vec![0_u8; 5 * 1024 * 1024]).expect("write full log");
        fs::write(backup_path(&path, LOG_BACKUPS), b"oldest").expect("write old backup");

        rotate_if_full(&path).expect("rotate log");

        assert!(!path.exists());
        assert_eq!(
            fs::metadata(backup_path(&path, 1)).expect("backup").len(),
            5 * 1024 * 1024
        );
        assert!(!backup_path(&path, LOG_BACKUPS + 1).exists());
        fs::remove_dir_all(directory).expect("remove test directory");
    }

    #[test]
    fn rotates_during_a_running_session() {
        let unique = SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "almagest-running-log-test-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&directory).expect("create test directory");
        let path = directory.join("app.log");
        let (mut log, error) = LogFile::open(path.clone()).expect("open log");
        assert!(error.is_none());
        log.file
            .as_mut()
            .expect("open file")
            .write_all(&vec![
                0_u8;
                usize::try_from(LOG_MAX_BYTES)
                    .expect("log size fits usize")
            ])
            .expect("fill log");

        log.write(1, format_args!("new log"));

        assert_eq!(
            fs::read_to_string(&path).expect("current log"),
            "1 new log\n"
        );
        assert_eq!(
            fs::metadata(backup_path(&path, 1)).expect("backup").len(),
            LOG_MAX_BYTES
        );
        drop(log);
        fs::remove_dir_all(directory).expect("remove test directory");
    }
}
