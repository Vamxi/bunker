//! What a tab is doing, read from /proc and the working tree: the
//! directory, the running command, the git branch, and the SSH host.

use std::path::{Path, PathBuf};

/// A snapshot of a tab's foreground process.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ProcInfo {
    /// The foreground process group.
    pub pgid: i32,
    /// The foreground process's working directory.
    pub cwd: Option<PathBuf>,
    /// The foreground command's name, when it is not the shell.
    pub command: Option<String>,
    /// The git branch (or short commit) of the working directory.
    pub git_branch: Option<String>,
    /// The host of a foreground `ssh`.
    pub ssh_host: Option<String>,
    /// The coding agent in the foreground, by name.
    pub agent: Option<&'static str>,
    /// The name of the program started in the tab, usually the shell.
    pub shell: Option<String>,
}

/// Coding agents by command name, with the name shown for them.
const AGENTS: &[(&str, &str)] = &[
    ("claude", "Claude Code"),
    ("codex", "Codex"),
    ("copilot", "Copilot"),
    ("gemini", "Gemini"),
    ("aider", "Aider"),
    ("opencode", "opencode"),
    ("cursor-agent", "Cursor"),
    ("amp", "Amp"),
    ("goose", "Goose"),
    ("qwen", "Qwen Code"),
    ("crush", "Crush"),
];

/// The agent a command line runs: by the program's name, or by the script
/// a runtime such as node runs.
fn agent(comm: Option<&str>, args: &[String]) -> Option<&'static str> {
    let base = |s: &str| {
        let name = s.rsplit('/').next().unwrap_or(s);
        name.trim_end_matches(".js").trim_end_matches(".mjs").trim_end_matches(".py").to_string()
    };
    let mut names: Vec<String> = comm.map(str::to_string).into_iter().collect();
    names.extend(args.iter().take(2).map(|a| base(a)));
    if args.iter().take(2).any(|a| a.contains("/@github/copilot")) {
        names.push("copilot".into());
    }
    names.iter().find_map(|n| AGENTS.iter().find(|(cmd, _)| cmd == n).map(|(_, name)| *name))
}

fn read_trimmed(path: impl AsRef<Path>) -> Option<String> {
    std::fs::read_to_string(path).ok().map(|s| s.trim().to_string())
}

/// The arguments of process `pid`.
fn cmdline(pid: i32) -> Vec<String> {
    std::fs::read(format!("/proc/{pid}/cmdline"))
        .map(|b| {
            b.split(|&c| c == 0).filter(|a| !a.is_empty()).map(|a| String::from_utf8_lossy(a).into_owned()).collect()
        })
        .unwrap_or_default()
}

/// The destination host of an ssh command line.
fn ssh_host(args: &[String]) -> Option<String> {
    // Options that take a value, from ssh(1).
    const WITH_VALUE: &str = "BbcDEeFIiJLlmOoPpQRSWw";
    let mut it = args.iter().skip(1);
    while let Some(a) = it.next() {
        if let Some(flags) = a.strip_prefix('-') {
            if let Some(last) = flags.chars().last()
                && WITH_VALUE.contains(last)
                && flags.len() == 1
            {
                it.next();
            }
            continue;
        }
        return Some(a.rsplit('@').next().unwrap_or(a).to_string());
    }
    None
}

/// The branch checked out in the repository containing `dir`.
pub fn git_branch(dir: &Path) -> Option<String> {
    let mut d = Some(dir);
    while let Some(cur) = d {
        let dotgit = cur.join(".git");
        let gitdir = if dotgit.is_dir() {
            Some(dotgit)
        } else if dotgit.is_file() {
            read_trimmed(&dotgit).and_then(|s| s.strip_prefix("gitdir:").map(|p| cur.join(p.trim())))
        } else {
            None
        };
        if let Some(g) = gitdir {
            let head = read_trimmed(g.join("HEAD"))?;
            return Some(match head.strip_prefix("ref: refs/heads/") {
                Some(branch) => branch.to_string(),
                None => head.chars().take(7).collect(),
            });
        }
        d = cur.parent();
    }
    None
}

/// Reads what the terminal's foreground process is doing. `shell` is the
/// process started in the tab.
pub fn read(pgid: i32, shell: i32) -> ProcInfo {
    let pid = if pgid > 0 { pgid } else { shell };
    let cwd = std::fs::read_link(format!("/proc/{pid}/cwd"))
        .or_else(|_| std::fs::read_link(format!("/proc/{shell}/cwd")))
        .ok();
    let command = (pid != shell).then(|| read_trimmed(format!("/proc/{pid}/comm"))).flatten();
    let args = if command.is_some() { cmdline(pid) } else { Vec::new() };
    let ssh_host = (command.as_deref() == Some("ssh")).then(|| ssh_host(&args)).flatten();
    let agent = command.as_deref().and_then(|c| agent(Some(c), &args));
    let git_branch = cwd.as_deref().and_then(git_branch);
    let shell = read_trimmed(format!("/proc/{shell}/comm"));
    ProcInfo { pgid, cwd, command, git_branch, ssh_host, agent, shell }
}

/// `path` with the home directory written as `~`.
pub fn tilde(path: &Path) -> String {
    if let Some(home) = std::env::var_os("HOME").map(PathBuf::from)
        && let Ok(rest) = path.strip_prefix(&home)
    {
        return if rest.as_os_str().is_empty() { "~".into() } else { format!("~/{}", rest.display()) };
    }
    path.display().to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ssh_hosts() {
        let cases: &[(&[&str], Option<&str>)] = &[
            (&["ssh", "host"], Some("host")),
            (&["ssh", "-p", "22", "me@box.lan"], Some("box.lan")),
            (&["ssh", "-At", "jump", "ls"], Some("jump")),
            (&["ssh"], None),
        ];
        for (args, want) in cases {
            let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
            assert_eq!(ssh_host(&args).as_deref(), *want, "{args:?}");
        }
    }

    #[test]
    fn agents() {
        let args = |a: &[&str]| a.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(agent(Some("claude"), &args(&["claude"])), Some("Claude Code"));
        assert_eq!(
            agent(Some("node"), &args(&["node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"])),
            Some("Codex")
        );
        assert_eq!(
            agent(Some("node"), &args(&["node", "/home/me/.npm/bin/@github/copilot/index.js"])),
            Some("Copilot")
        );
        assert_eq!(agent(Some("vim"), &args(&["vim", "claude.md"])), None);
    }

    #[test]
    fn git_branch_of_this_repo() {
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"));
        assert!(git_branch(dir).is_some());
    }

    #[test]
    fn tilde_paths() {
        let home = std::env::var("HOME").unwrap();
        assert_eq!(tilde(Path::new(&home)), "~");
        assert_eq!(tilde(&Path::new(&home).join("x")), "~/x");
        assert_eq!(tilde(Path::new("/etc")), "/etc");
    }
}
