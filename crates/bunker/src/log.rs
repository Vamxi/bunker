//! Logging: warnings and errors to stderr by default, everything to stderr
//! with --debug, and everything (including terminal output) to the trace
//! file with --trace.

use std::fs::{File, OpenOptions};
use std::io::Write;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::sync::Mutex;
use std::sync::atomic::{AtomicU8, Ordering};

/// How much is logged.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
#[repr(u8)]
pub enum Level {
    /// Something failed.
    Error = 1,
    /// Something is wrong but bunker carries on.
    Warn,
    /// Notable events.
    Info,
    /// Detail for debugging.
    Debug,
    /// Everything, including terminal data.
    Trace,
}

static STDERR_LEVEL: AtomicU8 = AtomicU8::new(Level::Warn as u8);
static FILE_LEVEL: AtomicU8 = AtomicU8::new(0);
static TRACE_FILE: Mutex<Option<File>> = Mutex::new(None);

/// Logs at Debug level to stderr.
pub fn enable_debug() {
    STDERR_LEVEL.store(Level::Debug as u8, Ordering::Relaxed);
}

/// Logs everything to `path`, truncating it. Trace logs contain terminal
/// output and /tmp is shared, so the file is only used if it is a regular
/// file this user owns, never through a symlink, at mode 0600.
pub fn enable_trace(path: &str) -> Result<(), String> {
    let file = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(false)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(path)
        .map_err(|e| format!("open trace log {path}: {e}"))?;
    let meta = file.metadata().map_err(|e| format!("stat trace log {path}: {e}"))?;
    // SAFETY: getuid has no preconditions.
    let uid = unsafe { libc::getuid() };
    if !meta.is_file() || meta.uid() != uid {
        return Err(format!("trace log {path} is not a regular file owned by you"));
    }
    file.set_len(0).map_err(|e| format!("truncate trace log {path}: {e}"))?;
    *TRACE_FILE.lock().unwrap_or_else(|e| e.into_inner()) = Some(file);
    FILE_LEVEL.store(Level::Trace as u8, Ordering::Relaxed);
    Ok(())
}

/// Reports whether a message at `level` would be written anywhere.
pub fn enabled(level: Level) -> bool {
    let l = level as u8;
    l <= STDERR_LEVEL.load(Ordering::Relaxed) || l <= FILE_LEVEL.load(Ordering::Relaxed)
}

/// Writes one log line; use the macros instead.
pub fn write(level: Level, args: std::fmt::Arguments) {
    let l = level as u8;
    let line = format!("{:<5} {args}\n", format!("{level:?}").to_uppercase());
    if l <= STDERR_LEVEL.load(Ordering::Relaxed) {
        let _ = std::io::stderr().write_all(line.as_bytes());
    }
    if l <= FILE_LEVEL.load(Ordering::Relaxed)
        && let Some(f) = TRACE_FILE.lock().unwrap_or_else(|e| e.into_inner()).as_mut()
    {
        let _ = f.write_all(line.as_bytes());
    }
}

/// Logs at a level, formatting only when enabled.
#[macro_export]
macro_rules! log_at {
    ($level:expr, $($arg:tt)*) => {
        if $crate::log::enabled($level) {
            $crate::log::write($level, format_args!($($arg)*));
        }
    };
}

/// Logs an error.
#[macro_export]
macro_rules! error { ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Error, $($arg)*) }; }
/// Logs a warning.
#[macro_export]
macro_rules! warn { ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Warn, $($arg)*) }; }
/// Logs at info level.
#[macro_export]
macro_rules! info { ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Info, $($arg)*) }; }
/// Logs at debug level.
#[macro_export]
macro_rules! debug { ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Debug, $($arg)*) }; }
/// Logs at trace level.
#[macro_export]
macro_rules! trace { ($($arg:tt)*) => { $crate::log_at!($crate::log::Level::Trace, $($arg)*) }; }
