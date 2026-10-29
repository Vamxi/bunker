//! Pseudo-terminals: starting a shell on one, resizing it, and asking which
//! process group is in the foreground.

use std::ffi::CStr;
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::{Child, Command, Stdio};

/// The TERM programs see.
pub const TERM_NAME: &str = "xterm-256color";

/// GLib's choice of notification backend, which bunker sets for itself.
pub const NOTIFY_BACKEND: &str = "GNOTIFICATION_BACKEND";

/// Set when bunker chose the backend, so programs it starts do not inherit
/// the choice.
pub static HIDE_NOTIFY_BACKEND: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

/// The controlling side of a pseudo-terminal.
#[derive(Debug)]
pub struct Pty {
    master: OwnedFd,
}

fn check(ret: libc::c_int, what: &str) -> io::Result<libc::c_int> {
    if ret < 0 {
        let e = io::Error::last_os_error();
        return Err(io::Error::new(e.kind(), format!("{what}: {e}")));
    }
    Ok(ret)
}

fn winsize(cols: u16, rows: u16, cell_w: u16, cell_h: u16) -> libc::winsize {
    libc::winsize { ws_row: rows, ws_col: cols, ws_xpixel: cols * cell_w, ws_ypixel: rows * cell_h }
}

impl Pty {
    /// Opens a pseudo-terminal pair and starts `argv` (or the user's shell)
    /// on it in `cwd`.
    pub fn spawn(argv: &[String], cwd: Option<&Path>, cols: u16, rows: u16) -> io::Result<(Self, Child)> {
        // SAFETY: plain libc calls on a descriptor we own; ptsname_r gets a
        // buffer of the size it is told.
        let (master, slave) = unsafe {
            let fd = check(libc::posix_openpt(libc::O_RDWR | libc::O_NOCTTY | libc::O_CLOEXEC), "posix_openpt")?;
            let master = OwnedFd::from_raw_fd(fd);
            check(libc::grantpt(fd), "grantpt")?;
            check(libc::unlockpt(fd), "unlockpt")?;
            let mut name = [0 as libc::c_char; 128];
            if libc::ptsname_r(fd, name.as_mut_ptr(), name.len()) != 0 {
                return Err(io::Error::other("ptsname_r failed"));
            }
            let path = CStr::from_ptr(name.as_ptr());
            let sfd = check(libc::open(path.as_ptr(), libc::O_RDWR | libc::O_NOCTTY | libc::O_CLOEXEC), "open pty")?;
            (master, OwnedFd::from_raw_fd(sfd))
        };
        let pty = Self { master };
        pty.resize(cols, rows, 0, 0)?;

        let shell = std::env::var("SHELL").ok().filter(|s| !s.is_empty()).unwrap_or_else(|| "/bin/sh".into());
        let (prog, args) = match argv.split_first() {
            Some((p, rest)) => (p.clone(), rest.to_vec()),
            None => (shell, Vec::new()),
        };
        let mut cmd = Command::new(&prog);
        cmd.args(&args)
            .stdin(Stdio::from(slave.try_clone()?))
            .stdout(Stdio::from(slave.try_clone()?))
            .stderr(Stdio::from(slave))
            .env("TERM", TERM_NAME)
            .env("COLORTERM", "truecolor")
            // Programs such as Neovim detect VTE features by version.
            .env("VTE_VERSION", "8203")
            // bunk and `bunker tui` refuse to nest inside bunker.
            .env("BUNK", "1")
            .env("TERM_PROGRAM", "bunker")
            .env_remove("COLUMNS")
            .env_remove("LINES");
        if HIDE_NOTIFY_BACKEND.load(std::sync::atomic::Ordering::Relaxed) {
            cmd.env_remove(NOTIFY_BACKEND);
        }
        if let Some(dir) = cwd.filter(|d| d.is_dir()) {
            cmd.current_dir(dir);
        }
        // SAFETY: only async-signal-safe calls between fork and exec.
        unsafe {
            cmd.pre_exec(|| {
                let mut set: libc::sigset_t = std::mem::zeroed();
                libc::sigemptyset(&mut set);
                libc::sigprocmask(libc::SIG_SETMASK, &set, std::ptr::null_mut());
                for sig in [libc::SIGPIPE, libc::SIGCHLD, libc::SIGINT, libc::SIGQUIT, libc::SIGTERM] {
                    libc::signal(sig, libc::SIG_DFL);
                }
                if libc::setsid() < 0 {
                    return Err(io::Error::last_os_error());
                }
                if libc::ioctl(0, libc::TIOCSCTTY, 0) < 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(())
            });
        }
        let child = cmd.spawn().map_err(|e| io::Error::new(e.kind(), format!("start {prog}: {e}")))?;
        Ok((pty, child))
    }

    /// Tells the program the new size.
    pub fn resize(&self, cols: u16, rows: u16, cell_w: u16, cell_h: u16) -> io::Result<()> {
        let ws = winsize(cols, rows, cell_w, cell_h);
        // SAFETY: TIOCSWINSZ reads a winsize from the pointer.
        check(unsafe { libc::ioctl(self.master.as_raw_fd(), libc::TIOCSWINSZ, &ws) }, "resize pty")?;
        Ok(())
    }

    /// The foreground process group, or 0 when unknown.
    pub fn foreground_pgid(&self) -> i32 {
        foreground_pgid(self.master.as_raw_fd())
    }

    /// A file for reading program output.
    pub fn reader(&self) -> io::Result<File> {
        Ok(File::from(self.master.try_clone()?))
    }

    /// A file for writing input.
    pub fn writer(&self) -> io::Result<File> {
        Ok(File::from(self.master.try_clone()?))
    }
}

/// The foreground process group of the terminal on `fd`, or 0.
pub fn foreground_pgid(fd: RawFd) -> i32 {
    // SAFETY: tcgetpgrp only reads the descriptor.
    let pgid = unsafe { libc::tcgetpgrp(fd) };
    pgid.max(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Read;

    #[test]
    fn runs_a_command() {
        let (pty, mut child) =
            Pty::spawn(&["/bin/sh".into(), "-c".into(), "printf \"$TERM $(stty size)\"".into()], None, 33, 7).unwrap();
        let mut reader = pty.reader().unwrap();
        let mut out = Vec::new();
        let mut buf = [0u8; 256];
        loop {
            match reader.read(&mut buf) {
                Ok(0) | Err(_) => break,
                Ok(n) => out.extend_from_slice(&buf[..n]),
            }
        }
        child.wait().unwrap();
        assert_eq!(String::from_utf8_lossy(&out), "xterm-256color 7 33");
    }
}
