package tcell

var keypadSS3 = map[rune]keyMap{
	'p': {Key: KeyRune, Rune: '0'}, 'q': {Key: KeyRune, Rune: '1'},
	'r': {Key: KeyRune, Rune: '2'}, 's': {Key: KeyRune, Rune: '3'},
	't': {Key: KeyRune, Rune: '4'}, 'u': {Key: KeyRune, Rune: '5'},
	'v': {Key: KeyRune, Rune: '6'}, 'w': {Key: KeyRune, Rune: '7'},
	'x': {Key: KeyRune, Rune: '8'}, 'y': {Key: KeyRune, Rune: '9'},
	'n': {Key: KeyRune, Rune: '.'}, 'o': {Key: KeyRune, Rune: '/'},
	'j': {Key: KeyRune, Rune: '*'}, 'm': {Key: KeyRune, Rune: '-'},
	'k': {Key: KeyRune, Rune: '+'}, 'l': {Key: KeyRune, Rune: ','},
	'X': {Key: KeyRune, Rune: '='}, 'M': {Key: KeyEnter},
}
