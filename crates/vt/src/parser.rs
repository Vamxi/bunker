//! The escape-sequence parser: a byte-level state machine after Paul
//! Williams' DEC VT parser, with a fast path for runs of printable text.
//!
//! Text, controls, and complete sequences go to a [`Perform`] in stream
//! order. String payloads (OSC, DCS, APC) are buffered whole and capped.

/// The most parameter values a control sequence keeps; later ones are dropped.
pub const MAX_PARAMS: usize = 32;

/// The longest OSC, DCS, or APC payload kept (an 8 MiB image transfer or
/// clipboard write); longer strings are discarded up to their terminator.
pub const MAX_STRING: usize = 8 << 20;

const MAX_INTERMEDIATES: usize = 4;

/// The receiver of everything the parser recognises.
pub trait Perform {
    /// A run of printable ASCII (0x20-0x7e).
    fn print_ascii(&mut self, text: &[u8]);
    /// One printable character outside ASCII.
    fn print(&mut self, c: char);
    /// A run of printable characters outside ASCII.
    fn print_str(&mut self, text: &str) {
        for c in text.chars() {
            self.print(c);
        }
    }
    /// A C0 control.
    fn execute(&mut self, byte: u8);
    /// A control sequence (CSI). `intermediates` starts with the private
    /// marker (`?`, `>`, `<`, `=`) when there is one.
    fn csi(&mut self, params: &Params, intermediates: &[u8], action: u8);
    /// An escape sequence.
    fn esc(&mut self, intermediates: &[u8], action: u8);
    /// An operating system command, without its terminator.
    fn osc(&mut self, data: &[u8], bell_terminated: bool);
    /// A device control string.
    fn dcs(&mut self, params: &Params, intermediates: &[u8], action: u8, data: &[u8]);
    /// An application program command (kitty graphics).
    fn apc(&mut self, data: &[u8]);
}

/// Control-sequence parameters, keeping colon-separated sub-parameters
/// (as in `4:3` or `38:2::255:0:0`) together with their parameter.
#[derive(Clone, Debug, Default)]
pub struct Params {
    values: [u16; MAX_PARAMS],
    /// Bit i is set when value i is a sub-parameter of the value before it.
    subs: u32,
    len: usize,
}

impl Params {
    /// The number of parameters, sub-parameters not counted separately.
    pub fn len(&self) -> usize {
        self.iter().count()
    }

    /// Reports whether there are no parameters.
    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    /// The first value of parameter `i`, or `default` when it is missing or 0.
    pub fn get(&self, i: usize, default: u16) -> u16 {
        match self.iter().nth(i).map(|p| p[0]) {
            Some(0) | None => default,
            Some(v) => v,
        }
    }

    /// The first value of parameter `i`, 0 when missing.
    pub fn raw(&self, i: usize) -> u16 {
        self.iter().nth(i).map_or(0, |p| p[0])
    }

    /// Iterates over parameters, each with its sub-parameters.
    pub fn iter(&self) -> ParamIter<'_> {
        ParamIter { params: self, pos: 0 }
    }

    fn clear(&mut self) {
        self.len = 0;
        self.subs = 0;
    }

    fn push(&mut self, value: u16, sub: bool) {
        if self.len < MAX_PARAMS {
            self.values[self.len] = value;
            if sub && self.len > 0 {
                self.subs |= 1 << self.len;
            }
            self.len += 1;
        }
    }

    /// Builds parameters from groups of values, for tests and replies.
    pub fn from_groups(groups: &[&[u16]]) -> Self {
        let mut p = Self::default();
        for g in groups {
            for (i, &v) in g.iter().enumerate() {
                p.push(v, i > 0);
            }
        }
        p
    }
}

/// Iterator over [`Params`] groups.
pub struct ParamIter<'a> {
    params: &'a Params,
    pos: usize,
}

impl<'a> Iterator for ParamIter<'a> {
    type Item = &'a [u16];

    fn next(&mut self) -> Option<&'a [u16]> {
        let p = self.params;
        if self.pos >= p.len {
            return None;
        }
        let start = self.pos;
        self.pos += 1;
        while self.pos < p.len && p.subs & (1 << self.pos) != 0 {
            self.pos += 1;
        }
        Some(&p.values[start..self.pos])
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Ground,
    Escape,
    EscapeIntermediate,
    CsiEntry,
    CsiParam,
    CsiIntermediate,
    CsiIgnore,
    DcsEntry,
    DcsParam,
    DcsIntermediate,
    DcsPassthrough,
    DcsIgnore,
    OscString,
    /// SOS, PM, or APC; only APC is kept.
    SosPmApcString,
}

/// The parser. It keeps partial sequences and UTF-8 between calls to
/// [`Parser::advance`], so input can be split anywhere.
pub struct Parser {
    state: State,
    params: Params,
    param: u16,
    /// A parameter digit or separator has been seen since the last push.
    param_started: bool,
    next_is_sub: bool,
    intermediates: [u8; MAX_INTERMEDIATES],
    n_intermediates: usize,
    string: Vec<u8>,
    string_overflow: bool,
    is_apc: bool,
    dcs_action: u8,
    utf8: [u8; 4],
    utf8_len: usize,
    utf8_need: usize,
}

impl Default for Parser {
    fn default() -> Self {
        Self {
            state: State::Ground,
            params: Params::default(),
            param: 0,
            param_started: false,
            next_is_sub: false,
            intermediates: [0; MAX_INTERMEDIATES],
            n_intermediates: 0,
            string: Vec::new(),
            string_overflow: false,
            is_apc: false,
            dcs_action: 0,
            utf8: [0; 4],
            utf8_len: 0,
            utf8_need: 0,
        }
    }
}

/// Position of the first C0 control or DEL in `bytes`, or its length.
#[inline]
fn find_control(bytes: &[u8]) -> usize {
    const LO: u64 = 0x0101_0101_0101_0101;
    const HI: u64 = 0x8080_8080_8080_8080;
    let mut i = 0;
    while i + 8 <= bytes.len() {
        let w = u64::from_le_bytes(bytes[i..i + 8].try_into().unwrap());
        // Bytes below 0x20 (exact for the lowest match), or equal to 0x7f.
        let below = w.wrapping_sub(LO * 0x20) & !w & HI;
        let x = w ^ (LO * 0x7f);
        let del = x.wrapping_sub(LO) & !x & HI;
        let hit = below | del;
        if hit != 0 {
            return i + (hit.trailing_zeros() / 8) as usize;
        }
        i += 8;
    }
    while i < bytes.len() {
        let b = bytes[i];
        if b < 0x20 || b == 0x7f {
            return i;
        }
        i += 1;
    }
    bytes.len()
}

impl Parser {
    /// Feeds bytes to the parser.
    pub fn advance<P: Perform>(&mut self, perform: &mut P, bytes: &[u8]) {
        let mut i = 0;
        while i < bytes.len() {
            if self.state == State::Ground && self.utf8_len == 0 {
                let end = i + find_control(&bytes[i..]);
                if end > i {
                    i = self.print_run(perform, bytes, i, end);
                    continue;
                }
                if bytes[i] == 0x1b
                    && bytes.get(i + 1) == Some(&b'[')
                    && let Some(n) = self.fast_csi(perform, &bytes[i + 2..])
                {
                    i += 2 + n;
                    continue;
                }
            }
            self.byte(perform, bytes[i]);
            i += 1;
        }
    }

    /// Prints text from `bytes[start..end]` (no controls) and returns where
    /// it stopped: at `end`, or at the start of an incomplete UTF-8
    /// sequence that it has saved for the next call.
    fn print_run<P: Perform>(&mut self, perform: &mut P, bytes: &[u8], start: usize, end: usize) -> usize {
        let mut i = start;
        while i < end {
            let ascii = bytes[i..end].iter().position(|&b| b >= 0x80).map_or(end, |n| i + n);
            if ascii > i {
                perform.print_ascii(&bytes[i..ascii]);
                i = ascii;
                continue;
            }
            match std::str::from_utf8(&bytes[i..end]) {
                Ok(s) => {
                    self.print_str(perform, s);
                    return end;
                }
                Err(e) => {
                    let valid = e.valid_up_to();
                    // SAFETY: from_utf8 validated these bytes.
                    let s = unsafe { std::str::from_utf8_unchecked(&bytes[i..i + valid]) };
                    self.print_str(perform, s);
                    i += valid;
                    match e.error_len() {
                        Some(n) => {
                            perform.print(char::REPLACEMENT_CHARACTER);
                            i += n;
                        }
                        None => {
                            // Incomplete at the end of the run: keep it for
                            // the bytes that follow.
                            for &b in &bytes[i..end] {
                                self.utf8_byte(perform, b);
                            }
                            return end;
                        }
                    }
                }
            }
        }
        end
    }

    /// Parses a whole control sequence whose bytes are all in `rest` (after
    /// `ESC [`) and dispatches it, returning its length. Returns `None` for
    /// anything unusual (an incomplete sequence, intermediates, controls
    /// inside) and leaves it to the state machine, which gives the same
    /// result byte by byte.
    #[inline]
    fn fast_csi<P: Perform>(&mut self, perform: &mut P, rest: &[u8]) -> Option<usize> {
        self.clear();
        let mut j = 0;
        if let Some(&b) = rest.first()
            && (0x3c..=0x3f).contains(&b)
        {
            self.collect(b);
            j = 1;
        }
        let (mut cur, mut started, mut sub) = (0u16, false, false);
        while let Some(&b) = rest.get(j) {
            match b {
                b'0'..=b'9' => {
                    cur = cur.saturating_mul(10).saturating_add(u16::from(b - b'0'));
                    started = true;
                }
                b';' | b':' => {
                    self.params.push(cur, sub);
                    cur = 0;
                    started = true;
                    sub = b == b':';
                }
                0x40..=0x7e => {
                    if started {
                        self.params.push(cur, sub);
                    }
                    let n = self.n_intermediates;
                    perform.csi(&self.params, &self.intermediates[..n], b);
                    self.clear();
                    return Some(j + 1);
                }
                _ => break,
            }
            j += 1;
        }
        self.clear();
        None
    }

    fn print_str<P: Perform>(&mut self, perform: &mut P, s: &str) {
        let b = s.as_bytes();
        let mut i = 0;
        while i < b.len() {
            if b[i] < 0x80 {
                let n = b[i..].iter().position(|&c| c >= 0x80).unwrap_or(b.len() - i);
                perform.print_ascii(&b[i..i + n]);
                i += n;
            } else {
                let n = b[i..].iter().position(|&c| c < 0x80).unwrap_or(b.len() - i);
                // SAFETY: the run starts and ends on char boundaries: after
                // ASCII or at the ends of `s`.
                perform.print_str(unsafe { s.get_unchecked(i..i + n) });
                i += n;
            }
        }
    }

    fn utf8_byte<P: Perform>(&mut self, perform: &mut P, b: u8) {
        if self.utf8_len == 0 {
            let need = match b {
                0xc2..=0xdf => 2,
                0xe0..=0xef => 3,
                0xf0..=0xf4 => 4,
                _ => {
                    perform.print(char::REPLACEMENT_CHARACTER);
                    return;
                }
            };
            self.utf8[0] = b;
            self.utf8_len = 1;
            self.utf8_need = need;
            return;
        }
        if b & 0xc0 != 0x80 {
            self.utf8_len = 0;
            perform.print(char::REPLACEMENT_CHARACTER);
            if b < 0x80 {
                self.byte(perform, b);
            } else {
                self.utf8_byte(perform, b);
            }
            return;
        }
        self.utf8[self.utf8_len] = b;
        self.utf8_len += 1;
        if self.utf8_len == self.utf8_need {
            let n = self.utf8_len;
            self.utf8_len = 0;
            match std::str::from_utf8(&self.utf8[..n]) {
                Ok(s) => perform.print(s.chars().next().unwrap()),
                Err(_) => perform.print(char::REPLACEMENT_CHARACTER),
            }
        }
    }

    fn clear(&mut self) {
        self.params.clear();
        self.param = 0;
        self.param_started = false;
        self.next_is_sub = false;
        self.n_intermediates = 0;
    }

    fn collect(&mut self, b: u8) {
        if self.n_intermediates < MAX_INTERMEDIATES {
            self.intermediates[self.n_intermediates] = b;
            self.n_intermediates += 1;
        }
    }

    fn param_byte(&mut self, b: u8) {
        match b {
            b'0'..=b'9' => {
                self.param = self.param.saturating_mul(10).saturating_add(u16::from(b - b'0'));
                self.param_started = true;
            }
            b';' | b':' => {
                self.params.push(self.param, self.next_is_sub);
                self.param = 0;
                self.param_started = true;
                self.next_is_sub = b == b':';
            }
            _ => {}
        }
    }

    fn finish_params(&mut self) {
        if self.param_started {
            self.params.push(self.param, self.next_is_sub);
        }
        self.param = 0;
        self.param_started = false;
        self.next_is_sub = false;
    }

    fn start_string(&mut self) {
        self.string.clear();
        self.string_overflow = false;
    }

    fn put_string(&mut self, b: u8) {
        if self.string.len() < MAX_STRING {
            self.string.push(b);
        } else {
            self.string_overflow = true;
        }
    }

    /// Ends the current string state, if any, dispatching its payload.
    fn end_string<P: Perform>(&mut self, perform: &mut P, bell: bool) {
        let state = self.state;
        self.state = State::Ground;
        if self.string_overflow {
            self.string.clear();
            return;
        }
        match state {
            State::OscString => perform.osc(&self.string, bell),
            State::DcsPassthrough => {
                let n = self.n_intermediates;
                perform.dcs(&self.params, &self.intermediates[..n], self.dcs_action, &self.string)
            }
            State::SosPmApcString if self.is_apc => perform.apc(&self.string),
            _ => {}
        }
        if self.string.capacity() > 64 << 10 {
            self.string = Vec::new();
        } else {
            self.string.clear();
        }
    }

    fn in_string(&self) -> bool {
        matches!(self.state, State::OscString | State::DcsPassthrough | State::DcsIgnore | State::SosPmApcString)
    }

    fn byte<P: Perform>(&mut self, perform: &mut P, b: u8) {
        if self.utf8_len > 0 && self.state == State::Ground {
            self.utf8_byte(perform, b);
            return;
        }
        // Transitions that apply in every state.
        match b {
            0x18 | 0x1a => {
                if self.in_string() {
                    self.string.clear();
                }
                self.state = State::Ground;
                if b == 0x1a {
                    perform.print(char::REPLACEMENT_CHARACTER);
                }
                return;
            }
            0x1b => {
                if self.in_string() {
                    self.end_string(perform, false);
                }
                self.clear();
                self.state = State::Escape;
                return;
            }
            _ => {}
        }
        match self.state {
            State::Ground => match b {
                0x00..=0x1f => perform.execute(b),
                0x7f => {}
                0x20..=0x7e => perform.print_ascii(&[b]),
                _ => self.utf8_byte(perform, b),
            },
            State::Escape => match b {
                0x00..=0x1f => perform.execute(b),
                0x20..=0x2f => {
                    self.collect(b);
                    self.state = State::EscapeIntermediate;
                }
                b'[' => self.state = State::CsiEntry,
                b']' => {
                    self.start_string();
                    self.state = State::OscString;
                }
                b'P' => self.state = State::DcsEntry,
                b'X' | b'^' | b'_' => {
                    self.start_string();
                    self.is_apc = b == b'_';
                    self.state = State::SosPmApcString;
                }
                0x30..=0x7e => {
                    self.state = State::Ground;
                    perform.esc(&[], b);
                }
                _ => {}
            },
            State::EscapeIntermediate => match b {
                0x00..=0x1f => perform.execute(b),
                0x20..=0x2f => self.collect(b),
                0x30..=0x7e => {
                    self.state = State::Ground;
                    let n = self.n_intermediates;
                    perform.esc(&self.intermediates[..n], b);
                }
                _ => {}
            },
            State::CsiEntry | State::CsiParam => match b {
                0x00..=0x1f => perform.execute(b),
                b'0'..=b'9' | b';' | b':' => {
                    self.param_byte(b);
                    self.state = State::CsiParam;
                }
                0x3c..=0x3f => {
                    if self.state == State::CsiEntry {
                        self.collect(b);
                        self.state = State::CsiParam;
                    } else {
                        self.state = State::CsiIgnore;
                    }
                }
                0x20..=0x2f => {
                    self.finish_params();
                    self.collect(b);
                    self.state = State::CsiIntermediate;
                }
                0x40..=0x7e => {
                    self.finish_params();
                    self.state = State::Ground;
                    let n = self.n_intermediates;
                    perform.csi(&self.params, &self.intermediates[..n], b);
                }
                _ => {}
            },
            State::CsiIntermediate => match b {
                0x00..=0x1f => perform.execute(b),
                0x20..=0x2f => self.collect(b),
                0x30..=0x3f => self.state = State::CsiIgnore,
                0x40..=0x7e => {
                    self.state = State::Ground;
                    let n = self.n_intermediates;
                    perform.csi(&self.params, &self.intermediates[..n], b);
                }
                _ => {}
            },
            State::CsiIgnore => match b {
                0x00..=0x1f => perform.execute(b),
                0x40..=0x7e => self.state = State::Ground,
                _ => {}
            },
            State::DcsEntry | State::DcsParam => match b {
                b'0'..=b'9' | b';' | b':' => {
                    self.param_byte(b);
                    self.state = State::DcsParam;
                }
                0x3c..=0x3f => {
                    if self.state == State::DcsEntry {
                        self.collect(b);
                        self.state = State::DcsParam;
                    } else {
                        self.state = State::DcsIgnore;
                    }
                }
                0x20..=0x2f => {
                    self.finish_params();
                    self.collect(b);
                    self.state = State::DcsIntermediate;
                }
                0x40..=0x7e => {
                    self.finish_params();
                    self.dcs_action = b;
                    self.start_string();
                    self.state = State::DcsPassthrough;
                }
                _ => {}
            },
            State::DcsIntermediate => match b {
                0x20..=0x2f => self.collect(b),
                0x30..=0x3f => self.state = State::DcsIgnore,
                0x40..=0x7e => {
                    self.dcs_action = b;
                    self.start_string();
                    self.state = State::DcsPassthrough;
                }
                _ => {}
            },
            State::DcsPassthrough => {
                if b != 0x7f {
                    self.put_string(b);
                }
            }
            State::DcsIgnore => {}
            State::OscString => match b {
                0x07 => self.end_string(perform, true),
                0x00..=0x1f => {}
                _ => self.put_string(b),
            },
            State::SosPmApcString => {
                if self.is_apc && b >= 0x20 {
                    self.put_string(b);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Default)]
    struct Log(Vec<String>);

    impl Perform for Log {
        fn print_ascii(&mut self, text: &[u8]) {
            match self.0.last_mut() {
                Some(last) if last.starts_with("text:") => last.push_str(std::str::from_utf8(text).unwrap()),
                _ => self.0.push(format!("text:{}", std::str::from_utf8(text).unwrap())),
            }
        }
        fn print(&mut self, c: char) {
            match self.0.last_mut() {
                Some(last) if last.starts_with("text:") => last.push(c),
                _ => self.0.push(format!("text:{c}")),
            }
        }
        fn execute(&mut self, byte: u8) {
            self.0.push(format!("exec:{byte:02x}"));
        }
        fn csi(&mut self, params: &Params, intermediates: &[u8], action: u8) {
            let p: Vec<String> =
                params.iter().map(|g| g.iter().map(u16::to_string).collect::<Vec<_>>().join(":")).collect();
            self.0.push(format!("csi:{}{}{}", String::from_utf8_lossy(intermediates), p.join(";"), action as char));
        }
        fn esc(&mut self, intermediates: &[u8], action: u8) {
            self.0.push(format!("esc:{}{}", String::from_utf8_lossy(intermediates), action as char));
        }
        fn osc(&mut self, data: &[u8], bell: bool) {
            self.0.push(format!("osc:{}:{bell}", String::from_utf8_lossy(data)));
        }
        fn dcs(&mut self, params: &Params, intermediates: &[u8], action: u8, data: &[u8]) {
            self.0.push(format!(
                "dcs:{}{:?}{}:{}",
                String::from_utf8_lossy(intermediates),
                params.iter().collect::<Vec<_>>(),
                action as char,
                String::from_utf8_lossy(data)
            ));
        }
        fn apc(&mut self, data: &[u8]) {
            self.0.push(format!("apc:{}", String::from_utf8_lossy(data)));
        }
    }

    fn parse(chunks: &[&[u8]]) -> Vec<String> {
        let mut p = Parser::default();
        let mut log = Log::default();
        for c in chunks {
            p.advance(&mut log, c);
        }
        log.0
    }

    #[test]
    fn sequences() {
        let cases: &[(&str, &[u8], &[&str])] = &[
            ("text", b"hello", &["text:hello"]),
            ("controls", b"a\r\nb", &["text:a", "exec:0d", "exec:0a", "text:b"]),
            ("csi", b"\x1b[1;31m", &["csi:1;31m"]),
            ("csi private", b"\x1b[?1049h", &["csi:?1049h"]),
            ("csi empty params", b"\x1b[;5H", &["csi:0;5H"]),
            ("csi no params", b"\x1b[m", &["csi:m"]),
            ("csi subparams", b"\x1b[4:3;38:2::1:2:3m", &["csi:4:3;38:2:0:1:2:3m"]),
            ("csi intermediate", b"\x1b[2 q", &["csi: 2q"]),
            ("csi kitty push", b"\x1b[>1u", &["csi:>1u"]),
            ("esc", b"\x1b7\x1b(0", &["esc:7", "esc:(0"]),
            ("osc bel", b"\x1b]0;title\x07", &["osc:0;title:true"]),
            ("osc st", b"\x1b]2;t\x1b\\", &["osc:2;t:false", "esc:\\"]),
            ("osc utf8", "\x1b]2;héllo\x07".as_bytes(), &["osc:2;héllo:true"]),
            ("dcs", b"\x1bP$qm\x1b\\", &["dcs:$[]q:m", "esc:\\"]),
            ("dcs params", b"\x1bP1;2q#0\x1b\\", &["dcs:[[1], [2]]q:#0", "esc:\\"]),
            ("apc", b"\x1b_Ga=q\x1b\\", &["apc:Ga=q", "esc:\\"]),
            ("utf8", "héllo 漢字".as_bytes(), &["text:héllo 漢字"]),
            ("invalid utf8", b"a\xffb", &["text:a\u{fffd}b"]),
            ("control inside csi", b"\x1b[1\n;2H", &["exec:0a", "csi:1;2H"]),
            ("can cancels", b"\x1b[1\x18x", &["text:x"]),
            ("del ignored", b"a\x7fb", &["text:ab"]),
        ];
        for (name, input, want) in cases {
            assert_eq!(parse(&[input]), *want, "{name}");
        }
    }

    #[test]
    fn split_anywhere() {
        let input = "a\x1b[38;2;1;2;3mé漢\x1b]0;t\x07😀z\x1b[?1049h\x1b[4:3m\x1b[2 q\x1b[1\n;2H\x1b[;5Hb".as_bytes();
        let whole = parse(&[input]);
        for cut in 1..input.len() {
            assert_eq!(parse(&[&input[..cut], &input[cut..]]), whole, "cut at {cut}");
        }
    }

    #[test]
    fn find_control_matches_scalar() {
        let mut data: Vec<u8> = (0..=255u8).collect();
        data.extend_from_slice(b"plain text without controls");
        for start in 0..data.len() {
            for end in start..data.len().min(start + 40) {
                let s = &data[start..end];
                let want = s.iter().position(|&b| b < 0x20 || b == 0x7f).unwrap_or(s.len());
                assert_eq!(find_control(s), want);
            }
        }
    }

    #[test]
    fn oversized_string_is_dropped() {
        let mut input = b"\x1b]52;c;".to_vec();
        input.resize(MAX_STRING + 100, b'A');
        input.extend_from_slice(b"\x07ok");
        assert_eq!(parse(&[&input]), vec!["text:ok"]);
    }

    #[test]
    fn param_overflow_saturates() {
        assert_eq!(parse(&[b"\x1b[99999999A"]), vec!["csi:65535A"]);
    }
}
