//! A running program on a pseudo-terminal: a thread reads its output into
//! the terminal, a thread writes input, and a thread watches what it is
//! doing. The UI is told through a callback and never waits on any of them.

use std::io::{self, Read, Write};
use std::path::Path;
use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, Mutex, MutexGuard, Weak};
use std::thread;
use std::time::Duration;

use bunker_vt::{Codec, MouseMode, Terminal, Theme};

use crate::procinfo::{self, ProcInfo};
use crate::pty::Pty;

/// How often the foreground process is checked.
const POLL_INTERVAL: Duration = Duration::from_secs(1);

/// The largest read from the program at once.
const READ_SIZE: usize = 64 << 10;

/// What the session tells the UI about.
#[derive(Debug)]
pub enum Wake {
    /// New output was parsed.
    Output,
    /// The foreground process or its directory changed.
    Info(ProcInfo),
    /// The program exited.
    Exited,
}

/// The UI's callback; it runs on the session's threads.
pub type Notify = Arc<dyn Fn(Wake) + Send + Sync>;

struct Shared {
    term: Mutex<Terminal>,
    pty: Pty,
    shell: i32,
    dead: AtomicBool,
    /// The process group that turned mouse or focus reporting on.
    report_owner: AtomicI32,
    /// The process group that set kitty keyboard flags.
    keyboard_owner: AtomicI32,
}

impl Shared {
    fn lock(&self) -> MutexGuard<'_, Terminal> {
        self.term.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// Turns off modes set by a process group that has left the
    /// foreground: after an ssh connection drops, the shell must not get
    /// mouse reports or kitty key codes it never asked for.
    fn drop_stale_modes(&self, term: &mut Terminal, fg: i32) {
        if fg <= 0 {
            return;
        }
        let owner = self.report_owner.load(Ordering::Relaxed);
        if owner > 0 && owner != fg {
            term.reset_report_modes();
            self.report_owner.store(0, Ordering::Relaxed);
        }
        let owner = self.keyboard_owner.load(Ordering::Relaxed);
        if owner > 0 && owner != fg {
            term.reset_keyboard_mode();
            self.keyboard_owner.store(0, Ordering::Relaxed);
        }
    }
}

fn reports_on(t: &Terminal) -> bool {
    t.modes().mouse != MouseMode::Off || t.modes().focus_events
}

/// A program running in a terminal.
pub struct Session {
    shared: Arc<Shared>,
    input: Sender<Vec<u8>>,
}

impl Session {
    /// Starts `argv` (the user's shell when empty) in `cwd`. `codec`
    /// decodes images the program shows.
    #[allow(clippy::too_many_arguments)]
    pub fn spawn(
        argv: &[String],
        cwd: Option<&Path>,
        cols: usize,
        lines: usize,
        scrollback: usize,
        theme: Theme,
        codec: Box<dyn Codec>,
        notify: Notify,
    ) -> io::Result<Self> {
        let (pty, mut child) = Pty::spawn(argv, cwd, cols as u16, lines as u16)?;
        let mut term = Terminal::new(cols, lines, scrollback);
        term.set_theme(theme);
        term.set_codec(codec);
        let shared = Arc::new(Shared {
            term: Mutex::new(term),
            shell: child.id() as i32,
            pty,
            dead: AtomicBool::new(false),
            report_owner: AtomicI32::new(0),
            keyboard_owner: AtomicI32::new(0),
        });
        let (tx, rx) = mpsc::channel::<Vec<u8>>();

        let mut writer = shared.pty.writer()?;
        thread::Builder::new().name("pty-writer".into()).spawn(move || {
            for bytes in rx {
                crate::trace!("pty write {:?}", String::from_utf8_lossy(&bytes));
                if let Err(e) = writer.write_all(&bytes) {
                    crate::trace!("pty write failed: {e}");
                    break;
                }
            }
        })?;

        let mut reader = shared.pty.reader()?;
        let (s, n, replies) = (shared.clone(), notify.clone(), tx.clone());
        thread::Builder::new().name("pty-reader".into()).spawn(move || {
            let mut buf = vec![0u8; READ_SIZE];
            loop {
                let len = match reader.read(&mut buf) {
                    Ok(0) => break,
                    Ok(len) => len,
                    Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                    Err(e) => {
                        crate::debug!("pty read ended: {e}");
                        break;
                    }
                };
                crate::trace!("pty read {:?}", String::from_utf8_lossy(&buf[..len]));
                let out = {
                    let mut term = s.lock();
                    let (reports, keyboard) = (reports_on(&term), term.kitty_flags() != 0);
                    term.advance(&buf[..len]);
                    if reports_on(&term) != reports || (term.kitty_flags() != 0) != keyboard {
                        let fg = s.pty.foreground_pgid();
                        let owner = |on: bool| if on { fg } else { 0 };
                        s.report_owner.store(owner(reports_on(&term)), Ordering::Relaxed);
                        s.keyboard_owner.store(owner(term.kitty_flags() != 0), Ordering::Relaxed);
                    }
                    term.take_replies()
                };
                if !out.is_empty() {
                    let _ = replies.send(out);
                }
                n(Wake::Output);
            }
            s.dead.store(true, Ordering::Relaxed);
            let _ = child.wait();
            n(Wake::Exited);
        })?;

        let weak: Weak<Shared> = Arc::downgrade(&shared);
        thread::Builder::new().name("proc-watch".into()).spawn(move || {
            let mut last = ProcInfo::default();
            loop {
                let Some(s) = weak.upgrade() else { return };
                if s.dead.load(Ordering::Relaxed) {
                    return;
                }
                let fg = s.pty.foreground_pgid();
                s.drop_stale_modes(&mut s.lock(), fg);
                let info = procinfo::read(fg, s.shell);
                drop(s);
                if info != last {
                    last = info.clone();
                    notify(Wake::Info(info));
                }
                thread::sleep(POLL_INTERVAL);
            }
        })?;

        Ok(Self { shared, input: tx })
    }

    /// Locks the terminal. Keep the lock short: the reader waits on it.
    pub fn term(&self) -> MutexGuard<'_, Terminal> {
        self.shared.lock()
    }

    /// Sends input to the program.
    pub fn write(&self, bytes: impl Into<Vec<u8>>) {
        let bytes = bytes.into();
        if !bytes.is_empty() {
            let _ = self.input.send(bytes);
        }
    }

    /// Sends mouse or focus reports, unless the program that asked for
    /// them has left the foreground.
    pub fn write_report(&self, term: &mut Terminal, bytes: Vec<u8>) {
        let fg = self.shared.pty.foreground_pgid();
        self.shared.drop_stale_modes(term, fg);
        if reports_on(term) {
            self.write(bytes);
        }
    }

    /// Resizes the terminal and tells the program.
    pub fn resize(&self, cols: usize, lines: usize, cell_w: u16, cell_h: u16) {
        {
            let mut t = self.term();
            t.resize(cols, lines);
            t.set_cell_pixels(cell_w, cell_h);
        }
        if let Err(e) = self.shared.pty.resize(cols as u16, lines as u16, cell_w, cell_h) {
            crate::debug!("{e}");
        }
    }

    /// Reports whether the program has exited.
    pub fn is_dead(&self) -> bool {
        self.shared.dead.load(Ordering::Relaxed)
    }

    /// The process started in the terminal.
    pub fn shell_pid(&self) -> i32 {
        self.shared.shell
    }

    /// The foreground process group.
    pub fn foreground_pgid(&self) -> i32 {
        self.shared.pty.foreground_pgid()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        if !self.is_dead() {
            // SAFETY: kill only sends a signal.
            unsafe {
                libc::kill(-self.shared.shell, libc::SIGHUP);
            }
        }
    }
}
