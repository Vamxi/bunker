//! Terminal behaviour tests: bytes in, screen and replies out.

use crate::*;

fn term(cols: usize, lines: usize) -> Terminal {
    Terminal::new(cols, lines, 100)
}

/// The screen as text, one line per row, trailing blanks trimmed.
fn screen(t: &Terminal) -> String {
    (0..t.lines())
        .map(|y| {
            let row = t.view_row(y);
            let mut s = String::new();
            for x in 0..row.cells().len() {
                row.push_cell_text(x, &mut s);
            }
            s.trim_end().to_string()
        })
        .collect::<Vec<_>>()
        .join("\n")
        .trim_end()
        .to_string()
}

fn history(t: &Terminal) -> Vec<String> {
    let first = t.first_row();
    (first..t.view_top())
        .map(|abs| {
            let row = t.row_at(abs).unwrap();
            let mut s = String::new();
            for x in 0..row.cells().len() {
                row.push_cell_text(x, &mut s);
            }
            s.trim_end().to_string()
        })
        .collect()
}

fn run(cols: usize, lines: usize, input: &str) -> Terminal {
    let mut t = term(cols, lines);
    t.advance(input.as_bytes());
    t
}

#[test]
fn screen_content() {
    let cases: &[(&str, &str, &str)] = &[
        ("plain", "hello", "hello"),
        ("newline", "a\r\nb", "a\nb"),
        ("wrap", "abcdefgh", "abcde\nfgh"),
        ("cursor position", "\x1b[2;3Hx", "\n  x"),
        ("erase line", "abcde\x1b[3G\x1b[K", "ab"),
        ("erase line start", "abcde\x1b[3G\x1b[1K", "   de"),
        ("erase display", "a\r\nb\x1b[2J", ""),
        ("insert chars", "abcd\x1b[2G\x1b[2@", "a  bc"),
        ("delete chars", "abcde\x1b[2G\x1b[2P", "ade"),
        ("erase chars", "abcde\x1b[2G\x1b[2X", "a  de"),
        ("backspace", "ab\x08c", "ac"),
        ("tab", "a\tb", "a   b"),
        ("tab stop", "\x1b[3G\x1bH\ra\tb", "a b"),
        ("repeat", "x\x1b[3b", "xxxx"),
        ("insert mode", "abc\x1b[1G\x1b[4hX", "Xabc"),
        ("dec graphics", "\x1b(0qx\x1b(Bq", "─│q"),
        ("wide", "漢字", "漢字"),
        ("wide wraps", "abcd漢", "abcd\n漢"),
        ("combining", "e\u{301}x", "e\u{301}x"),
        ("emoji zwj", "👨\u{200d}👩x", "👨\u{200d}👩x"),
        ("flag", "🇳🇱x", "🇳🇱x"),
        ("vs16 widens", "❤\u{fe0f}x", "❤\u{fe0f}x"),
        ("overwrite wide half", "漢\x1b[2Gx", " x"),
        ("insert lines", "a\r\nb\r\nc\x1b[2H\x1b[L", "a\n\nb\nc"),
        ("delete lines", "a\r\nb\r\nc\x1b[1H\x1b[M", "b\nc"),
        ("no autowrap", "\x1b[?7labcdefg", "abcdg"),
        ("decaln", "\x1b#8", "EEEEE\nEEEEE\nEEEEE\nEEEEE"),
        ("reverse index at top", "a\x1b[H\x1bMb", "b\na"),
    ];
    for (name, input, want) in cases {
        let t = run(5, 4, input);
        assert_eq!(screen(&t), *want, "{name}");
    }
}

#[test]
fn wide_chars_take_two_cells() {
    let t = run(10, 2, "漢x");
    assert_eq!(t.cursor(), (3, 0));
    let row = t.view_row(0);
    assert!(row.cells()[0].is_wide());
    assert!(row.cells()[1].is_spacer());
    assert_eq!(row.cells()[2].char(), 'x');
}

#[test]
fn vs16_moves_cursor() {
    let t = run(10, 2, "❤\u{fe0f}");
    assert_eq!(t.cursor(), (2, 0));
}

#[test]
fn scrolling_fills_history() {
    let t = run(5, 2, "1\r\n2\r\n3\r\n4");
    assert_eq!(screen(&t), "3\n4");
    assert_eq!(history(&t), vec!["1", "2"]);
}

#[test]
fn scroll_region() {
    let t = run(5, 4, "\x1b[2;3r\x1b[2Ha\r\nb\r\nc");
    assert_eq!(screen(&t), "\nb\nc");
    assert!(history(&t).is_empty(), "lines leaving a region below the top are not saved");
}

#[test]
fn erase_scrollback() {
    let mut t = run(5, 2, "1\r\n2\r\n3");
    t.advance(b"\x1b[3J");
    assert!(history(&t).is_empty());
    assert!(t.take_events().contains(&Event::ScrollbackCleared));
}

#[test]
fn alt_screen() {
    let mut t = run(5, 2, "main");
    t.advance(b"\x1b[?1049h\x1b[Halt");
    assert!(t.alt_screen());
    assert_eq!(screen(&t), "alt");
    t.advance(b"\x1b[?1049l");
    assert_eq!(screen(&t), "main");
    assert_eq!(t.cursor(), (4, 0));
}

#[test]
fn alt_screen_keeps_no_history() {
    let t = run(5, 2, "\x1b[?1049h1\r\n2\r\n3");
    assert_eq!(t.history(), 0);
}

#[test]
fn sgr_styles() {
    let t = run(20, 1, "\x1b[1;3;4:3;38;2;1;2;3;48;5;200;58:2::9:8:7mx\x1b[0my");
    let row = t.view_row(0);
    let s = t.style(row.cells()[0].style);
    assert!(s.attrs.contains(Attrs::BOLD) && s.attrs.contains(Attrs::ITALIC));
    assert_eq!(s.underline, Underline::Curly);
    assert_eq!(s.fg, Color::Rgb(1, 2, 3));
    assert_eq!(s.bg, Color::Indexed(200));
    assert_eq!(s.underline_color, Color::Rgb(9, 8, 7));
    assert_eq!(*t.style(row.cells()[1].style), Style::default());
}

#[test]
fn sgr_bright_and_resets() {
    let t = run(20, 1, "\x1b[91;102;2;9mx\x1b[22;29;39;49my");
    let row = t.view_row(0);
    let a = t.style(row.cells()[0].style);
    assert_eq!((a.fg, a.bg), (Color::Indexed(9), Color::Indexed(10)));
    assert!(a.attrs.contains(Attrs::DIM) && a.attrs.contains(Attrs::STRIKE));
    assert_eq!(*t.style(row.cells()[1].style), Style::default());
}

#[test]
fn erase_uses_background() {
    let t = run(5, 1, "\x1b[44m\x1b[K");
    let row = t.view_row(0);
    assert_eq!(t.style(row.cells()[4].style).bg, Color::Indexed(4));
}

#[test]
fn replies() {
    let cases: &[(&str, &str)] = &[
        ("\x1b[c", "\x1b[?62;4;22c"),
        ("\x1b[>c", "\x1b[>0;279;0c"),
        ("\x1b[=c", "\x1bP!|00000000\x1b\\"),
        ("\x1b[5n", "\x1b[0n"),
        ("\x1b[3;4H\x1b[6n", "\x1b[3;4R"),
        ("\x1b[3;4H\x1b[?6n", "\x1b[?3;4R"),
        ("\x1b[2;4r\x1b[?6h\x1b[2;1H\x1b[6n", "\x1b[2;1R"),
        ("\x1b[>q", "\x1bP>|VTE(8203)\x1b\\"),
        ("\x1b[?2004h\x1b[?2004$p", "\x1b[?2004;1$y"),
        ("\x1b[?1000$p", "\x1b[?1000;2$y"),
        ("\x1b[?9999$p", "\x1b[?9999;0$y"),
        ("\x1b[4$p", "\x1b[4;2$y"),
        ("\x1b[?u", "\x1b[?0u"),
        ("\x1b[>5u\x1b[?u", "\x1b[?5u"),
        ("\x1b[>5u\x1b[=4;3u\x1b[?u", "\x1b[?1u"),
        ("\x1b[>5u\x1b[<u\x1b[?u", "\x1b[?0u"),
        ("\x1b]10;?\x07", "\x1b]10;rgb:ffff/ffff/ffff\x07"),
        ("\x1b]11;?\x1b\\", "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\"),
        ("\x1b]4;1;?\x07", "\x1b]4;1;rgb:c0c0/1c1c/2828\x07"),
        ("\x1b]4;1;#010203\x07\x1b]4;1;?\x07", "\x1b]4;1;rgb:0101/0202/0303\x07"),
        ("\x1b[18t", "\x1b[8;24;80t"),
        ("\x1bP$qm\x1b\\", "\x1bP1$r0m\x1b\\"),
        ("\x1b[1;31m\x1bP$qm\x1b\\", "\x1bP1$r0;1;31m\x1b\\"),
        ("\x1b[5;20r\x1bP$qr\x1b\\", "\x1bP1$r5;20r\x1b\\"),
        ("\x1b[4 q\x1bP$q q\x1b\\", "\x1bP1$r4 q\x1b\\"),
        ("\x1bP$qx\x1b\\", "\x1bP0$r\x1b\\"),
        ("\x1bP+q544e\x1b\\", "\x1bP1+r544E=787465726D2D323536636F6C6F72\x1b\\"),
        ("\x1bP+q6162\x1b\\", "\x1bP0+r6162\x1b\\"),
        ("\x1b[?996n", "\x1b[?997;1n"),
        ("\x1b]52;c;?\x07", ""),
    ];
    for (input, want) in cases {
        let mut t = term(80, 24);
        t.advance(input.as_bytes());
        assert_eq!(String::from_utf8(t.take_replies()).unwrap(), *want, "{input:?}");
    }
}

#[test]
fn events() {
    let cases: &[(&str, Event)] = &[
        ("\x07", Event::Bell),
        ("\x1b]2;hi\x07", Event::TitleChanged),
        ("\x1b]7;file://h/tmp\x07", Event::CwdChanged),
        ("\x1b]9;done\x07", Event::Notify { title: String::new(), body: "done".into() }),
        ("\x1b]9;4;1;50\x07", Event::Progress { state: 1, percent: 50 }),
        ("\x1b]777;notify;T;B\x07", Event::Notify { title: "T".into(), body: "B".into() }),
        ("\x1b]99;;hello\x1b\\", Event::Notify { title: String::new(), body: "hello".into() }),
        ("\x1b]99;d=0:p=title;T\x1b\\\x1b]99;p=body;B\x1b\\", Event::Notify { title: "T".into(), body: "B".into() }),
        ("\x1b]52;c;aGk=\x07", Event::Clipboard("hi".into())),
        ("\x1b]133;D;1\x07", Event::CommandFinished(Some(1))),
        ("\x1b[?1000h", Event::ReportModesChanged),
    ];
    for (input, want) in cases {
        let mut t = term(80, 24);
        t.advance(input.as_bytes());
        assert!(t.take_events().contains(want), "{input:?} should give {want:?}");
    }
}

#[test]
fn title_and_cwd() {
    let t = run(80, 2, "\x1b]0;my title\x07\x1b]7;file://host/home/me/a%20b\x1b\\");
    assert_eq!(t.title(), "my title");
    assert_eq!(t.cwd(), Some("/home/me/a b"));
}

#[test]
fn title_stack() {
    let t = run(80, 2, "\x1b]2;one\x07\x1b[22t\x1b]2;two\x07\x1b[23t");
    assert_eq!(t.title(), "one");
}

#[test]
fn hyperlinks() {
    let t = run(20, 1, "\x1b]8;;https://x.y\x1b\\link\x1b]8;;\x1b\\ no");
    let row = t.view_row(0);
    let link = t.style(row.cells()[0].style).link;
    assert_eq!(t.link(link), Some("https://x.y"));
    assert_eq!(t.style(row.cells()[5].style).link, 0);
}

#[test]
fn modes() {
    let t = run(80, 2, "\x1b[?1h\x1b=\x1b[?1002h\x1b[?1006h\x1b[?2004h\x1b[?25l\x1b[?1004h");
    let m = t.modes();
    assert!(m.app_cursor && m.app_keypad && m.mouse_sgr && m.bracketed_paste && m.focus_events);
    assert_eq!(m.mouse, MouseMode::Drag);
    assert!(!m.cursor_visible);
    let t = run(80, 2, "\x1b[?1002h\x1b[?1000l");
    assert_eq!(t.modes().mouse, MouseMode::Drag, "resetting another mouse mode leaves the current one");
}

#[test]
fn cursor_style() {
    let t = run(80, 2, "\x1b[5 q");
    assert_eq!(t.cursor_shape(), CursorShape::Bar);
    assert!(t.modes().cursor_blink);
    let t = run(80, 2, "\x1b[2 q");
    assert_eq!(t.cursor_shape(), CursorShape::Block);
    assert!(!t.modes().cursor_blink);
}

#[test]
fn cursor_blink_request() {
    assert_eq!(run(80, 2, "").cursor_blink(), None, "no request: the desktop decides");
    assert_eq!(run(80, 2, "\x1b[5 q").cursor_blink(), Some(true));
    assert_eq!(run(80, 2, "\x1b[4 q").cursor_blink(), Some(false));
    assert_eq!(run(80, 2, "\x1b[2 q\x1b[0 q").cursor_blink(), None, "DECSCUSR 0 is the default again");
    assert_eq!(run(80, 2, "\x1b[?12l").cursor_blink(), None, "cnorm's DECRST 12 is not a request");
    assert_eq!(run(80, 2, "\x1b[1 q\x1bc").cursor_blink(), None, "reset forgets it");
}

#[test]
fn full_reset() {
    let mut t = run(5, 2, "1\r\n2\r\n3\x1b]2;t\x07\x1b[?1h");
    t.advance(b"\x1bc");
    assert_eq!(screen(&t), "");
    assert_eq!(t.history(), 0);
    assert_eq!(t.title(), "");
    assert!(!t.modes().app_cursor);
}

#[test]
fn prompt_marks() {
    let mut t = run(10, 3, "\x1b]133;A\x07$ \x1b]133;B\x07false\r\n\x1b]133;C\x07out\r\n\x1b]133;D;1\x07");
    assert!(t.view_row(0).flags.contains(RowFlags::PROMPT));
    assert!(t.view_row(1).flags.contains(RowFlags::OUTPUT));
    assert!(t.view_row(1).flags.contains(RowFlags::FAILED));
    assert!(t.take_events().contains(&Event::CommandFinished(Some(1))));
}

#[test]
fn reflow_narrower_and_back() {
    let mut t = run(10, 3, "abcdefghij\r\nxy");
    t.resize(5, 3);
    assert_eq!(screen(&t), "abcde\nfghij\nxy");
    assert_eq!(t.cursor(), (2, 2));
    t.resize(10, 3);
    assert_eq!(screen(&t), "abcdefghij\nxy");
    assert_eq!(t.cursor(), (2, 1));
}

#[test]
fn reflow_pushes_to_history() {
    let mut t = run(10, 2, "abcdefghij\r\nxy");
    t.resize(5, 2);
    assert_eq!(history(&t), vec!["abcde"]);
    assert_eq!(screen(&t), "fghij\nxy");
    t.resize(10, 2);
    assert!(history(&t).is_empty());
    assert_eq!(screen(&t), "abcdefghij\nxy");
}

#[test]
fn reflow_wide_char_at_edge() {
    let mut t = run(6, 4, "abcd漢x\r\n");
    t.resize(5, 4);
    assert_eq!(screen(&t), "abcd\n漢x");
    t.resize(6, 4);
    assert_eq!(screen(&t), "abcd漢\nx");
}

#[test]
fn reflow_keeps_graphemes() {
    let mut t = run(4, 4, "abce\u{301}fg\r\n");
    t.resize(3, 4);
    assert_eq!(screen(&t), "abc\ne\u{301}fg");
}

#[test]
fn taller_window_pulls_history_back() {
    let mut t = run(5, 2, "1\r\n2\r\n3\r\n4");
    t.resize(5, 4);
    assert_eq!(screen(&t), "1\n2\n3\n4");
    assert_eq!(t.cursor(), (1, 3));
    t.resize(5, 2);
    assert_eq!(screen(&t), "3\n4");
}

#[test]
fn shorter_window_drops_blank_rows_first() {
    let mut t = run(5, 5, "a\r\nb");
    t.resize(5, 2);
    assert_eq!(screen(&t), "a\nb");
    assert_eq!(t.history(), 0);
}

#[test]
fn reflow_with_alt_screen_keeps_primary_cursor() {
    let mut t = run(10, 3, "abcdefghij\r\nxy\x1b[?1049h");
    t.resize(5, 3);
    t.advance(b"\x1b[?1049l");
    assert_eq!(t.cursor(), (2, 2));
}

#[test]
fn selection_text() {
    let t = run(5, 4, "hello world\r\nab  ");
    let top = t.view_top();
    assert_eq!(t.text(Point::new(top, 0), Point::new(top + 3, 4)), "hello world\nab");
    assert_eq!(t.text(Point::new(top, 3), Point::new(top, 4)), "lo");
    assert_eq!(t.block_text(Point::new(top, 0), Point::new(top + 1, 1)), "he\n w");
}

#[test]
fn word_and_line_bounds() {
    let t = run(8, 3, "ls /usr/local/bin x");
    let top = t.view_top();
    let (a, b) = t.word_bounds(Point::new(top, 5));
    assert_eq!(t.text(a, b), "/usr/local/bin");
    let (a, b) = t.line_bounds(Point::new(top + 1, 0));
    assert_eq!(t.text(a, b), "ls /usr/local/bin x");
}

#[test]
fn view_stays_put_while_output_scrolls() {
    let mut t = run(5, 2, "1\r\n2\r\n3\r\n4");
    t.scroll_display(2);
    assert_eq!(screen(&t), "1\n2");
    t.advance(b"\r\n5\r\n6");
    assert_eq!(screen(&t), "1\n2");
    t.scroll_to_bottom();
    assert_eq!(screen(&t), "5\n6");
}

#[test]
fn style_table_compacts_when_full() {
    let mut t = term(10, 2);
    for i in 0..70_000u32 {
        let (r, g, b) = ((i >> 16) as u8, (i >> 8) as u8, i as u8);
        t.advance(format!("\x1b[38;2;{r};{g};{b}mx").as_bytes());
    }
    t.advance(b"\x1b[38;2;1;2;3mz");
    let (x, y) = t.cursor();
    let row = t.view_row(y);
    assert_eq!(t.style(row.cells()[x - 1].style).fg, Color::Rgb(1, 2, 3));
}

#[test]
fn row_ids_change_only_with_content() {
    let mut t = run(5, 3, "a\r\nb");
    let ids: Vec<u64> = (0..3).map(|y| t.view_row(y).id()).collect();
    t.advance(b"\x1b[3;1Hc");
    assert_eq!(t.view_row(0).id(), ids[0]);
    assert_eq!(t.view_row(1).id(), ids[1]);
    assert_ne!(t.view_row(2).id(), ids[2]);
    t.advance(b"\r\n");
    assert_eq!(t.view_row(0).id(), ids[1], "scrolling moves rows without changing them");
}

#[test]
fn arbitrary_input_never_panics() {
    // A small xorshift generator: deterministic, no dependency.
    let mut x: u64 = 0x9e37_79b9_7f4a_7c15;
    let mut next = || {
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        x
    };
    let pieces: &[&[u8]] = &[
        b"\x1b[",
        b"\x1b]",
        b"\x1bP",
        b";",
        b":",
        b"?",
        b"1",
        b"9",
        b"m",
        b"H",
        b"J",
        b"K",
        b"L",
        b"M",
        b"@",
        b"P",
        b"r",
        b"h",
        b"l",
        b"\x07",
        b"\r\n",
        "漢".as_bytes(),
        "e\u{301}".as_bytes(),
        "👨\u{200d}".as_bytes(),
        b"\xff",
        b"abc",
        b"\x1b\\",
        b"\x08",
        b"\t",
        b"\x1b7",
        b"\x1b8",
        b"\x1bM",
        b"\x1b[?1049h",
        b"\x1b[?1049l",
        b"\x1b[4h",
        b"\x1b[?7l",
    ];
    for round in 0..200 {
        let mut t = Terminal::new(7 + round % 5, 3 + round % 4, 10);
        let mut input = Vec::new();
        for _ in 0..400 {
            input.extend_from_slice(pieces[(next() % pieces.len() as u64) as usize]);
        }
        for chunk in input.chunks(1 + (next() % 17) as usize) {
            t.advance(chunk);
            if next() % 50 == 0 {
                t.resize(2 + (next() % 12) as usize, 1 + (next() % 6) as usize);
            }
        }
        let (x, y) = t.cursor();
        assert!(x < t.cols() && y < t.lines());
        for y in 0..t.lines() {
            assert_eq!(t.view_row(y).cells().len(), t.cols());
        }
    }
}

/// Replays what bash's line editor sends after a resize: it assumes the
/// terminal did not rewrap its line (as xterm), moves up to where the line
/// began at the old width, clears, and prints it again.
fn readline_redraw(t: &mut Terminal, line: &str, old_width: usize) {
    let up = (line.chars().count().max(1) - 1) / old_width;
    let mut out = String::new();
    if up > 0 {
        out.push_str(&format!("\x1b[{up}A"));
    }
    out.push_str("\r\x1b[K\r");
    out.push_str(line);
    out.push_str("\x1b[J");
    t.advance(out.as_bytes());
}

#[test]
fn prompt_survives_resizes() {
    let prompt = "vamsi@vamsi-fedora:~$ ";
    let mut t = run(40, 6, &format!("ls\r\nCargo.toml\r\n{prompt}"));
    let mut width = 40;
    for w in [30, 12, 9, 25, 7, 40, 15, 40] {
        t.resize(w, 6);
        readline_redraw(&mut t, prompt, width);
        width = w;
    }
    let text = screen(&t);
    assert_eq!(text.matches("vamsi@").count(), 1, "prompt duplicated after resizing:\n{text}");
    assert!(text.ends_with(prompt.trim_end()), "{text}");
    assert!(text.starts_with("ls\nCargo.toml"), "history above the prompt is kept:\n{text}");
}

#[test]
fn live_line_is_cut_not_rewrapped() {
    let mut t = run(10, 3, "done\r\n$ abcdefgh");
    t.resize(6, 3);
    assert_eq!(screen(&t), "done\n$ abcd", "the cursor's line is cut, as xterm does");
    assert_eq!(t.cursor(), (5, 1));
}

#[test]
fn sixel_image_is_placed_and_scrolls() {
    let mut t = term(20, 5);
    t.set_cell_pixels(2, 6);
    t.advance(b"ab\x1bPq#1;2;100;0;0#1!4~-#1!4~\x1b\\after");
    let images = t.images_in_view();
    assert_eq!(images.len(), 1);
    let (y, p) = &images[0];
    assert_eq!((*y, p.col, p.cols, p.rows), (0, 2, 2, 2));
    assert_eq!(t.cursor(), (7, 2), "inline images leave the cursor below, at their left");
    t.advance(b"\r\n\r\n\r\n");
    let (y, _) = t.images_in_view()[0];
    assert_eq!(y, -1, "the image scrolled with its row");
}

#[test]
fn clearing_the_screen_removes_images() {
    let mut t = term(20, 5);
    t.set_cell_pixels(2, 6);
    t.advance(b"\x1bPq#1!4~\x1b\\");
    assert_eq!(t.images_in_view().len(), 1);
    t.advance(b"\x1b[2J");
    assert!(t.images_in_view().is_empty());
}

#[test]
fn kitty_image_placed_and_deleted() {
    let mut t = term(20, 5);
    t.set_cell_pixels(2, 4);
    t.advance(b"\x1b_Ga=T,f=24,s=2,v=1,i=3,q=2;/wAAAP8A\x1b\\");
    assert_eq!(t.images_in_view().len(), 1);
    assert_eq!(t.cursor(), (1, 0), "kitty leaves the cursor after the image");
    t.advance(b"\x1b_Ga=d,d=i,i=3\x1b\\");
    assert!(t.images_in_view().is_empty());
}

#[test]
fn images_survive_reflow() {
    let mut t = term(20, 5);
    t.set_cell_pixels(2, 6);
    t.advance(b"\x1bPq#1!4~\x1b\\x\r\n");
    t.resize(12, 5);
    assert_eq!(t.images_in_view().len(), 1);
}
