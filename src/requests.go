package main

// Field masks and sync envelopes of the Google Photos mobile protocol.
var mediaMask = message(
	b(1, message(
		b(1, nil),
		b(3, nil),
		b(4, nil),
		b(5, message(b(1, nil), b(2, nil), b(3, nil), b(4, nil), b(5, nil), b(7, nil))),
		b(6, nil),
		b(7, message(b(2, nil))),
		b(15, nil),
		b(16, nil),
		b(17, nil),
		b(19, nil),
		b(20, nil),
		b(21, message(b(5, message(b(3, nil))), b(6, nil))),
		b(25, nil),
		b(30, message(b(2, nil))),
		b(31, nil),
		b(32, nil),
		b(33, message(b(1, nil))),
		b(34, nil),
		b(36, nil),
		b(37, nil),
		b(38, nil),
		b(39, nil),
		b(40, nil),
		b(41, nil),
	)),
	b(5, message(
		b(2, message(
			b(2, message(b(3, message(b(2, nil))), b(4, message(b(2, nil))))),
			b(4, message(b(2, message(v(2, 1))))),
			b(5, message(b(2, nil))),
			v(6, 1),
		)),
		b(3, message(
			b(2, message(b(3, nil), b(4, nil))),
			b(3, message(b(2, nil), b(3, message(v(2, 1))))),
			b(4, nil),
			b(5, message(b(2, message(v(2, 1))))),
			b(7, nil),
		)),
		b(4, message(b(2, message(b(2, nil))))),
		b(5, message(
			b(1, message(
				b(2, message(b(3, nil), b(4, nil))),
				b(3, message(b(2, nil), b(3, message(v(2, 1))))),
			)),
			v(3, 1),
		)),
	)),
	b(8, nil),
	b(9, message(
		b(2, nil),
		b(3, message(b(1, nil), b(2, nil))),
		b(4, message(
			b(1, message(
				b(3, message(
					b(1, message(
						b(1, message(b(5, message(b(1, nil))), b(6, nil))),
						b(2, nil),
						b(3, message(b(1, message(b(5, message(b(1, nil))), b(6, nil))), b(2, nil))),
					)),
				)),
				b(4, message(b(1, message(b(2, nil))))),
			)),
		)),
	)),
	b(11, message(b(2, nil), b(3, nil), b(4, message(b(2, message(v(1, 1), v(2, 2))))))),
	b(12, nil),
	b(14, message(b(2, nil), b(3, nil), b(4, message(b(2, message(v(1, 1), v(2, 2))))))),
	b(15, message(b(1, nil), b(4, nil))),
	b(17, message(b(1, nil), b(4, nil))),
	b(19, message(b(2, nil), b(3, nil), b(4, message(b(2, message(v(1, 1), v(2, 2))))))),
	b(22, nil),
	b(23, nil),
)

var collectionMask = message(
	b(1, message(
		b(2, nil),
		b(3, nil),
		b(4, nil),
		b(5, nil),
		b(6, message(b(1, nil), b(2, nil), b(3, nil), b(4, nil), b(5, nil), b(7, nil))),
		b(7, nil),
		b(8, nil),
		b(10, nil),
		b(12, nil),
		b(13, message(b(2, nil), b(3, nil))),
		b(15, message(b(1, nil))),
		b(18, nil),
	)),
	b(4, message(b(1, nil))),
	b(9, nil),
	b(11, message(b(1, message(b(1, nil), b(4, nil), b(5, nil), b(6, nil), b(9, nil))))),
	b(17, nil),
	b(18, message(b(1, nil), b(2, message(b(1, nil))))),
	b(20, message(b(2, message(b(1, nil), b(2, nil))))),
	b(23, nil),
)

var responseMask = message(
	b(2, nil),
	b(3, message(
		b(2, nil),
		b(3, nil),
		b(7, nil),
		b(8, nil),
		b(14, message(b(1, nil))),
		b(16, nil),
		b(17, message(b(2, nil))),
		b(18, nil),
		b(19, nil),
		b(20, nil),
		b(21, nil),
		b(22, nil),
		b(23, nil),
		b(27, message(b(1, nil), b(2, message(b(1, nil))))),
		b(29, nil),
		b(30, nil),
		b(31, nil),
		b(32, nil),
		b(34, nil),
		b(37, nil),
		b(38, nil),
		b(39, nil),
		b(41, nil),
	)),
	b(4, message(b(2, nil), b(3, message(b(1, nil))), b(4, nil), b(5, message(b(1, nil))))),
	b(7, nil),
	b(12, nil),
	b(13, nil),
	b(15, nil),
	b(18, nil),
	b(20, nil),
	b(24, nil),
	b(25, nil),
)

var syncOptions = message(
	b(1, message(b(2, message(b(1, nil), b(2, nil))))),
	b(2, message(b(3, message(v(2, 1))))),
	b(3, message(b(2, nil))),
	b(4, nil),
	b(7, message(b(1, nil))),
	b(8, message(v(1, 2), b(2, []byte{0x01, 0x02, 0x03, 0x05, 0x06}))),
	b(9, nil),
)

var recordMask = message(b(2, message(b(1, nil), b(2, nil))), b(3, message(b(1, nil))), b(4, nil))

var queryOptions = message(b(3, message(v(1, 1))))

var collectionMaskPaged = message(
	b(1, message(
		b(2, nil),
		b(3, nil),
		b(4, nil),
		b(5, nil),
		b(6, message(b(1, nil), b(2, nil), b(3, nil), b(4, nil), b(5, nil), b(7, nil))),
		b(7, nil),
		b(8, nil),
		b(10, nil),
		b(12, nil),
		b(13, message(b(2, nil), b(3, nil))),
		b(15, message(b(1, nil))),
		b(18, nil),
	)),
	b(4, message(b(1, nil))),
	b(9, nil),
	b(17, nil),
	b(18, message(b(1, nil), b(2, message(b(1, nil))))),
	b(20, message(b(2, message(b(1, nil), b(2, nil))))),
	b(23, nil),
)

var responseMaskPaged = message(
	b(2, nil),
	b(3, message(
		b(2, nil),
		b(3, nil),
		b(7, nil),
		b(8, nil),
		b(14, message(b(1, nil))),
		b(16, nil),
		b(17, message(b(2, nil))),
		b(18, nil),
		b(19, nil),
		b(20, nil),
		b(21, nil),
		b(22, nil),
		b(23, nil),
		b(27, message(b(1, nil), b(2, message(b(1, nil))))),
		b(29, nil),
		b(30, nil),
		b(31, nil),
		b(32, nil),
		b(34, nil),
		b(37, nil),
		b(38, nil),
		b(39, nil),
		b(41, nil),
	)),
	b(4, message(b(2, nil), b(3, nil), b(4, nil))),
	b(7, nil),
	b(12, nil),
	b(13, nil),
	b(15, nil),
	b(18, nil),
	b(20, nil),
	b(24, nil),
	b(25, nil),
)

func libraryState(stateToken string) []byte {
	return wrapLibrary(message(
		b(1, mediaMask),
		b(2, collectionMask),
		b(3, responseMask),
		optionalText(6, stateToken),
		v(7, 2),
		b(9, syncOptions),
		v(11, 1),
		v(11, 2),
		v(11, 6),
		b(12, recordMask),
		b(13, nil),
		b(15, queryOptions),
	))
}

func libraryInitialPage(pageToken string) []byte {
	return wrapLibrary(message(
		b(1, mediaMask),
		b(2, collectionMaskPaged),
		b(3, responseMaskPaged),
		optionalText(4, pageToken),
		v(7, 2),
		b(9, syncOptions),
		v(11, 1),
		v(11, 2),
		b(12, recordMask),
		b(13, nil),
		b(15, queryOptions),
	))
}

func libraryDeltaPage(pageToken, stateToken string) []byte {
	return wrapLibrary(message(
		b(1, mediaMask),
		b(2, collectionMaskPaged),
		b(3, responseMaskPaged),
		optionalText(4, pageToken),
		optionalText(6, stateToken),
		v(7, 2),
		b(9, syncOptions),
		v(11, 1),
		v(11, 2),
		b(12, recordMask),
		b(13, nil),
		b(15, queryOptions),
	))
}

func wrapLibrary(content []byte) []byte {
	leaf := message(b(1, nil))
	selection := message(b(1, leaf), b(2, nil))
	options := message(b(1, message(b(1, selection))), b(2, nil))
	return message(b(1, content), b(2, options))
}
