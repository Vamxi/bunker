//! Measures emulator throughput on recorded workloads and the cost of a
//! reflow, the same measurements as the Go version's BenchmarkPaneWrite
//! and BenchmarkReflow.
//!
//!   cargo run --release -p bunker-vt --example throughput -- DIR
//!
//! DIR holds the workloads as NAME.bin files.

use std::time::{Duration, Instant};

use bunker_vt::Terminal;

fn time<F: FnMut()>(mut f: F) -> Duration {
    let mut best = Duration::MAX;
    for _ in 0..7 {
        let start = Instant::now();
        f();
        best = best.min(start.elapsed());
    }
    best
}

fn main() {
    let dir = std::env::args().nth(1).expect("usage: throughput DIR");
    for name in ["seq", "prose", "colors", "unicode", "tui"] {
        let path = format!("{dir}/{name}.bin");
        let Ok(data) = std::fs::read(&path) else {
            eprintln!("skip {path}");
            continue;
        };
        let mut t = Terminal::new(120, 40, 10_000);
        let d = time(|| t.advance(&data));
        let mbs = data.len() as f64 / d.as_secs_f64() / 1e6;
        println!("{name:8} {:>8.2} ms  {mbs:>8.1} MB/s", d.as_secs_f64() * 1e3);
    }
    if let Ok(prose) = std::fs::read(format!("{dir}/prose.bin")) {
        let lines: Vec<&[u8]> = prose.split_inclusive(|&b| b == b'\n').take(5_000).collect();
        let input: Vec<u8> = lines.concat();
        let mut wide = true;
        let mut t = Terminal::new(120, 40, 10_000);
        t.advance(&input);
        let d = time(|| {
            t.resize(if wide { 100 } else { 120 }, 40);
            wide = !wide;
        });
        println!("reflow   {:>8.2} ms  ({} rows of history)", d.as_secs_f64() * 1e3, t.history());
    }
}
