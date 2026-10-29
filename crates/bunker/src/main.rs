//! bunker: a GTK 4 terminal for Linux.

mod cli;
mod config;
mod log;
mod procinfo;
mod pty;
mod session;
mod theme;
mod ui;

use std::path::PathBuf;
use std::process::ExitCode;

/// The version: the release's (`make release` sets BUNKER_VERSION), or
/// the crate's for a development build.
pub const VERSION: &str = match option_env!("BUNKER_VERSION") {
    Some(v) => v,
    None => env!("CARGO_PKG_VERSION"),
};

const USAGE: &str = "Usage: bunker [--config PATH] [--debug] [--trace] [--version] [-- COMMAND [ARGS...]]
       bunker config init [--force] | list | get SETTING | set SETTING VALUE
       bunker themes
       bunker install-desktop [--data-dir DIR] [--exec PATH]

  -c, --config PATH   config file (default ~/.config/bunker/config.toml)
  -d, --debug         log to stderr
      --trace         log everything to /tmp/bunker.log
      --version       print the version
  -- COMMAND ARGS     run COMMAND in the first tab instead of the shell
";

struct Args {
    config: Option<PathBuf>,
    debug: bool,
    trace: bool,
    command: Vec<String>,
    /// A subcommand (config, themes, install-desktop) and its arguments.
    sub: Vec<String>,
}

fn parse_args() -> Result<Option<Args>, String> {
    let mut args = Args { config: None, debug: false, trace: false, command: Vec::new(), sub: Vec::new() };
    let mut it = std::env::args().skip(1);
    while let Some(a) = it.next() {
        match a.as_str() {
            "-c" | "--config" => args.config = Some(it.next().ok_or("--config needs a path")?.into()),
            "-d" | "--debug" => args.debug = true,
            "--trace" => args.trace = true,
            "--" => {
                args.command = it.by_ref().collect();
                if args.command.is_empty() {
                    return Err(format!("-- needs a command\n\n{USAGE}"));
                }
            }
            "--version" => {
                println!("bunker {VERSION}");
                return Ok(None);
            }
            "-h" | "--help" => {
                print!("{USAGE}");
                return Ok(None);
            }
            other if args.sub.is_empty() && !other.starts_with('-') => {
                args.sub.push(other.to_string());
                args.sub.extend(it.by_ref());
            }
            other => return Err(format!("unknown argument {other:?}\n\n{USAGE}")),
        }
    }
    Ok(Some(args))
}

fn main() -> ExitCode {
    let args = match parse_args() {
        Ok(Some(a)) => a,
        Ok(None) => return ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("bunker: {e}");
            return ExitCode::from(2);
        }
    };
    if args.debug {
        log::enable_debug();
    }
    if !args.sub.is_empty() {
        // Commands print to pipes (`bunker config list | head`): a closed
        // pipe ends them quietly, as it does other command-line tools,
        // instead of failing a print.
        // SAFETY: no other threads exist yet.
        unsafe { libc::signal(libc::SIGPIPE, libc::SIG_DFL) };
        return match cli::run(&args.sub, args.config.as_deref()) {
            Some(Ok(())) => ExitCode::SUCCESS,
            Some(Err(e)) => {
                eprintln!("bunker: {e}");
                ExitCode::FAILURE
            }
            None => {
                eprintln!("bunker: unknown command {:?}\n\n{USAGE}", args.sub[0]);
                ExitCode::from(2)
            }
        };
    }
    if args.trace
        && let Err(e) = log::enable_trace("/tmp/bunker.log")
    {
        eprintln!("bunker: {e}");
        return ExitCode::FAILURE;
    }
    // Windows are not unique, so GNOME could not call back a clicked
    // notification; the freedesktop backend delivers the click in-process.
    if std::env::var_os(pty::NOTIFY_BACKEND).is_none() {
        // SAFETY: no other thread exists yet.
        unsafe { std::env::set_var(pty::NOTIFY_BACKEND, "freedesktop") };
        pty::HIDE_NOTIFY_BACKEND.store(true, std::sync::atomic::Ordering::Relaxed);
    }
    let explicit = args.config.is_some();
    let path = args.config.unwrap_or_else(config::default_path);
    let config = match config::load(&path, explicit) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("bunker: {e}");
            return ExitCode::FAILURE;
        }
    };
    info!("config {} theme {} font {}", path.display(), config.theme, config.font);
    ui::run(config, args.command).into()
}
