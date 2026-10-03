# Changelog

## [0.18.0](https://github.com/reyer3/bunker-go/compare/v0.17.0...v0.18.0) (2026-10-03)


### Features

* **mcp:** attachments on send/reply and a download tool ([#140](https://github.com/reyer3/bunker-go/issues/140)) ([b3de1f5](https://github.com/reyer3/bunker-go/commit/b3de1f55085b178950c006bc8a6b0b77d989fdf7))
* **whatsapp:** echo cancellation for calls ([#141](https://github.com/reyer3/bunker-go/issues/141)) ([b7c833c](https://github.com/reyer3/bunker-go/commit/b7c833cc3ee935e2addba3f02e2c808c4ee01602))

## [0.17.0](https://github.com/reyer3/bunker-go/compare/v0.16.0...v0.17.0) (2026-10-02)


### Features

* **tui:** wrap sidebar previews onto two lines ([#138](https://github.com/reyer3/bunker-go/issues/138)) ([c269a84](https://github.com/reyer3/bunker-go/commit/c269a84f54273e16998891507d36052665ff3db4))


### Bug Fixes

* **matrix:** fall back to legacy media download on servers without authenticated media ([#137](https://github.com/reyer3/bunker-go/issues/137)) ([9c54683](https://github.com/reyer3/bunker-go/commit/9c54683abfe2cd9af12eafc6c391037d212ab36b))

## [0.16.0](https://github.com/reyer3/bunker-go/compare/v0.15.0...v0.16.0) (2026-10-02)


### Features

* **herdr:** dock the bunker panel on the right edge ([#136](https://github.com/reyer3/bunker-go/issues/136)) ([39f235f](https://github.com/reyer3/bunker-go/commit/39f235f80728b008f32726f65e0244f725721eb1))
* **tui:** copy messages, open attachments by double-click, send with one Enter ([#134](https://github.com/reyer3/bunker-go/issues/134)) ([544bcc9](https://github.com/reyer3/bunker-go/commit/544bcc9d370f19ec76c991226103b640d27d4b65))
* upcoming meetings from invitations, with one-click join ([#135](https://github.com/reyer3/bunker-go/issues/135)) ([f153bb9](https://github.com/reyer3/bunker-go/commit/f153bb9ed29d23f7a02b7cd9c67b34a88cfe1f19))


### Bug Fixes

* send Office documents with their own MIME type, not application/zip ([#132](https://github.com/reyer3/bunker-go/issues/132)) ([2fa7fa1](https://github.com/reyer3/bunker-go/commit/2fa7fa1b0c11d20c5a96c698428fc3958b1fa466))

## [0.15.0](https://github.com/reyer3/bunker-go/compare/v0.14.0...v0.15.0) (2026-10-02)


### Features

* **tui:** message previews in the sidebar and conversation lists ([#131](https://github.com/reyer3/bunker-go/issues/131)) ([2cfd431](https://github.com/reyer3/bunker-go/commit/2cfd431940fcb3ebbca481eef260f0f7a0b7418f))
* voice notes — receive, play, record and send ([#128](https://github.com/reyer3/bunker-go/issues/128)) ([b355f30](https://github.com/reyer3/bunker-go/commit/b355f30f357e7ba9b3d536702d6b8821e8ab5f7d))


### Bug Fixes

* **tui:** stop mouse wheel reports leaking into the composer as text ([#130](https://github.com/reyer3/bunker-go/issues/130)) ([f742b54](https://github.com/reyer3/bunker-go/commit/f742b545ad15adf7e0df40343ed15d162531f602))

## [0.14.0](https://github.com/reyer3/bunker-go/compare/v0.13.0...v0.14.0) (2026-10-02)


### Features

* **tui:** shortcuts to search a contact (@) and to call (c) ([#126](https://github.com/reyer3/bunker-go/issues/126)) ([209c3ec](https://github.com/reyer3/bunker-go/commit/209c3ec12dfeba212809bfc7d1aa231b9b67d597))
* **tui:** WhatsApp-style chat list in the WhatsApp and Matrix tabs ([#125](https://github.com/reyer3/bunker-go/issues/125)) ([d8b6794](https://github.com/reyer3/bunker-go/commit/d8b679456e4c18743799eb2cadc91a3df8176f7a))


### Bug Fixes

* **herdr:** reuse one conversation pane instead of opening duplicates ([#124](https://github.com/reyer3/bunker-go/issues/124)) ([504a50f](https://github.com/reyer3/bunker-go/commit/504a50f49f15d1fb99685e6c62a874bc5303a18a))
* **tui:** wrap chat messages to the pane width and resize the composer ([#122](https://github.com/reyer3/bunker-go/issues/122)) ([9291d2c](https://github.com/reyer3/bunker-go/commit/9291d2c5048ce8f7e7685e219984d0f5f1ea3362))
* **whatsapp:** make silent call audio diagnosable and fail loudly ([#127](https://github.com/reyer3/bunker-go/issues/127)) ([2e418ce](https://github.com/reyer3/bunker-go/commit/2e418cee98603efbd62770e7a3822a811e0cc7df))

## [0.13.0](https://github.com/reyer3/bunker-go/compare/v0.12.0...v0.13.0) (2026-09-30)


### ⚠ BREAKING CHANGES

* **cli:** `bunker read` no longer marks the item read unless --mark-read is passed. Plan/Receipt JSON keys are now snake_case (id, recipients, fanout_pause_min, ...) and empty optional fields are omitted.

### Features

* **cli:** consistent JSON output and read without side effects ([#121](https://github.com/reyer3/bunker-go/issues/121)) ([e5e5394](https://github.com/reyer3/bunker-go/commit/e5e539479d2137660ed4e28b858fb67428afa720))
* **tui:** command palette with Ctrl+K ([#117](https://github.com/reyer3/bunker-go/issues/117)) ([87e0776](https://github.com/reyer3/bunker-go/commit/87e0776b2cb01e05f3068c961f662c24189928bc))
* **tui:** voice calls — place, answer, reject, hang up ([#118](https://github.com/reyer3/bunker-go/issues/118)) ([e84aca4](https://github.com/reyer3/bunker-go/commit/e84aca491fcc76186208461c8c8780658903996c))


### Bug Fixes

* **matrix:** Send returns the item id, so sent messages can be edited at once ([#115](https://github.com/reyer3/bunker-go/issues/115)) ([13e8e64](https://github.com/reyer3/bunker-go/commit/13e8e64001c282d339036c43144c4a714cfeb231))
* **tui:** refresh an open chat so incoming messages show up ([#119](https://github.com/reyer3/bunker-go/issues/119)) ([c1bd51d](https://github.com/reyer3/bunker-go/commit/c1bd51d875e80724b2322de42dcbb3cf1dd217b9))

## [0.12.0](https://github.com/reyer3/bunker-go/compare/v0.11.0...v0.12.0) (2026-09-29)


### Features

* bunker version, release checks and bunker update ([#112](https://github.com/reyer3/bunker-go/issues/112)) ([ac12440](https://github.com/reyer3/bunker-go/commit/ac124409df52383266975e8830032c9c51ff6b03))
* edit, delete and react to messages on WhatsApp and Matrix ([#113](https://github.com/reyer3/bunker-go/issues/113)) ([db65158](https://github.com/reyer3/bunker-go/commit/db6515811185dbb50d663c32c7b28f2053ff6ca1))
* **matrix:** handle inbound edits, reactions and redactions ([#108](https://github.com/reyer3/bunker-go/issues/108)) ([c02ded2](https://github.com/reyer3/bunker-go/commit/c02ded2cec35e796b94a8382102d07257ad792f8))
* **mcp:** read attachment text ([#111](https://github.com/reyer3/bunker-go/issues/111)) ([2542176](https://github.com/reyer3/bunker-go/commit/2542176393e2cbc3340cf81cda0a31ff96e4de7e))
* **whatsapp:** preview media from the message's own thumbnail ([#110](https://github.com/reyer3/bunker-go/issues/110)) ([0452a5a](https://github.com/reyer3/bunker-go/commit/0452a5ad535a51aa00e0911d5af9e8ffbe1d14ca))

## [0.11.0](https://github.com/reyer3/bunker-go/compare/v0.10.0...v0.11.0) (2026-09-29)


### Features

* **herdr:** ask Claude about the open conversation, unread counts and notifications ([#103](https://github.com/reyer3/bunker-go/issues/103)) ([16a25ac](https://github.com/reyer3/bunker-go/commit/16a25acb62ae73e5bc016b4da0857a844333d99a))
* **tui:** query language in / and the mail folder on rows ([#104](https://github.com/reyer3/bunker-go/issues/104)) ([3999d09](https://github.com/reyer3/bunker-go/commit/3999d094ff6aafb5d12347040f569e5d2b57b486))


### Bug Fixes

* **matrix:** mark-unread no longer claims a server change it did not make ([#101](https://github.com/reyer3/bunker-go/issues/101)) ([64a270b](https://github.com/reyer3/bunker-go/commit/64a270b62dcd860ac9fbd4ef601e719f90e1777c))

## [0.10.0](https://github.com/reyer3/bunker-go/compare/v0.9.0...v0.10.0) (2026-09-29)


### Features

* idempotent sends and MCP organize tools ([#97](https://github.com/reyer3/bunker-go/issues/97)) ([faac90b](https://github.com/reyer3/bunker-go/commit/faac90beedb122b1ceb0520b4cbea05c6ab9a3cc))
* query language, cursor pagination and MCP search tools ([#99](https://github.com/reyer3/bunker-go/issues/99)) ([2b8764b](https://github.com/reyer3/bunker-go/commit/2b8764bb7a7ed6b6cbe83382fa0a1ae611423595))

## [0.9.0](https://github.com/reyer3/bunker-go/compare/v0.8.0...v0.9.0) (2026-09-29)


### Features

* **mail:** archive on Gmail moves the message to All Mail ([#93](https://github.com/reyer3/bunker-go/issues/93)) ([825dfea](https://github.com/reyer3/bunker-go/commit/825dfeaea1d77a0d7f2abe277632942c5ee55de4))
* **mail:** store mail bodies so search finds them ([#95](https://github.com/reyer3/bunker-go/issues/95)) ([71495c4](https://github.com/reyer3/bunker-go/commit/71495c481ae9d1482e3942cbf70f13662e06bdbc))
* **mail:** sync every relevant folder and follow moves between them ([#96](https://github.com/reyer3/bunker-go/issues/96)) ([93fcf61](https://github.com/reyer3/bunker-go/commit/93fcf6140c099451e490c69de5c26b7165c36215))

## [0.8.0](https://github.com/reyer3/bunker-go/compare/v0.7.0...v0.8.0) (2026-09-29)


### Features

* **app:** report launcher errors via notify-send and a log file ([#85](https://github.com/reyer3/bunker-go/issues/85)) ([ccd0ab7](https://github.com/reyer3/bunker-go/commit/ccd0ab79603bd58bad2cd1ca1d55ae8d0516d15c))
* herdr plugin and bunker herdr toggle ([#87](https://github.com/reyer3/bunker-go/issues/87)) ([43eb476](https://github.com/reyer3/bunker-go/commit/43eb4764a8aec5267a3d726b19e7f26365ad2332))
* **mcp:** bunker mcp serves the inbox to AI agents over MCP ([#32](https://github.com/reyer3/bunker-go/issues/32)) ([d5be817](https://github.com/reyer3/bunker-go/commit/d5be817c3ce18a3007fe64d03af51ac857722d78)), closes [#31](https://github.com/reyer3/bunker-go/issues/31)
* **mcp:** health tool with daemon and adapter status ([#84](https://github.com/reyer3/bunker-go/issues/84)) ([56f9768](https://github.com/reyer3/bunker-go/commit/56f9768c076b094d20f26bc650a7f5da317780af))
* **store:** FTS5 full-text index with accent folding ([#89](https://github.com/reyer3/bunker-go/issues/89)) ([0a0eb16](https://github.com/reyer3/bunker-go/commit/0a0eb165e4f29e31d2eafb4d6d5179e358a5214c))
* **tui:** / filters the inbox; chats show loading and empty states ([#45](https://github.com/reyer3/bunker-go/issues/45)) ([8ebb8db](https://github.com/reyer3/bunker-go/commit/8ebb8db863ca72bc79d3bf50289e12b206c58206)), closes [#39](https://github.com/reyer3/bunker-go/issues/39)
* **tui:** bunker sidebar and bunker open for the herdr panel ([#92](https://github.com/reyer3/bunker-go/issues/92)) ([f763cbe](https://github.com/reyer3/bunker-go/commit/f763cbea6f3fea80e2d831cc1272e69e6acd83e7))
* **tui:** one send gesture and one hint notation in every view ([#44](https://github.com/reyer3/bunker-go/issues/44)) ([bffb363](https://github.com/reyer3/bunker-go/commit/bffb363ffe705f00a2b2ddef49438edf2542b189))
* **tui:** per-view key hints and a contextual, scrollable help ([#42](https://github.com/reyer3/bunker-go/issues/42)) ([f3454e7](https://github.com/reyer3/bunker-go/commit/f3454e74971a1855e5396cfdcc91ef0ff9fc1a1c)), closes [#36](https://github.com/reyer3/bunker-go/issues/36)
* **tui:** show connection status and recover on its own ([#41](https://github.com/reyer3/bunker-go/issues/41)) ([3a589b0](https://github.com/reyer3/bunker-go/commit/3a589b02d740e453bba211f1e74c84764884268d)), closes [#35](https://github.com/reyer3/bunker-go/issues/35)
* **tui:** undo mark-read with u, keep drafts, bunker unread ([#43](https://github.com/reyer3/bunker-go/issues/43)) ([92c8c7d](https://github.com/reyer3/bunker-go/commit/92c8c7dbee21489908bb71742ae718c834c1b2f3))


### Bug Fixes

* **mail:** resolve Archive via \Archive or the prefixed folder ([#86](https://github.com/reyer3/bunker-go/issues/86)) ([138f24f](https://github.com/reyer3/bunker-go/commit/138f24fed9e90ef18771460c6edf965e7fbd93b3))
* **store:** apply the thread filter and escape LIKE patterns ([#83](https://github.com/reyer3/bunker-go/issues/83)) ([d2ae7a8](https://github.com/reyer3/bunker-go/commit/d2ae7a837edb7046f5c2c67bb69d2bc83bc79b52))
* **tui:** every screen in Spanish, errors in human terms ([#40](https://github.com/reyer3/bunker-go/issues/40)) ([9a64017](https://github.com/reyer3/bunker-go/commit/9a64017e1d42e8e607fa86a46f1b3ebccdcf46a0)), closes [#34](https://github.com/reyer3/bunker-go/issues/34)
* **whatsapp:** log dropped store errors and bound the presence revoke ([#88](https://github.com/reyer3/bunker-go/issues/88)) ([68cd061](https://github.com/reyer3/bunker-go/commit/68cd0611d54a619cde81bad442d6211f68322371))

## [0.7.0](https://github.com/reyer3/bunker-go/compare/v0.6.0...v0.7.0) (2026-09-28)


### Features

* **app:** bunker app opens the TUI in its own terminal window ([#23](https://github.com/reyer3/bunker-go/issues/23)) ([8b54fee](https://github.com/reyer3/bunker-go/commit/8b54feed49c7910c0ffdb139a8c861dc5ef55bd0)), closes [#16](https://github.com/reyer3/bunker-go/issues/16)
* **contacts:** list contacts and accept names in send and call ([#29](https://github.com/reyer3/bunker-go/issues/29)) ([5a07103](https://github.com/reyer3/bunker-go/commit/5a0710393c730360fe6bdddfa195987527b75268)), closes [#25](https://github.com/reyer3/bunker-go/issues/25)
* **render:** show calls in the tmux segment; call actions accept latest ([#22](https://github.com/reyer3/bunker-go/issues/22)) ([86611ba](https://github.com/reyer3/bunker-go/commit/86611bad67032c45a2fa57fb18e17e7aee47730b)), closes [#15](https://github.com/reyer3/bunker-go/issues/15)
* **tui:** start a new conversation from a contact picker (n) ([#30](https://github.com/reyer3/bunker-go/issues/30)) ([5e82d94](https://github.com/reyer3/bunker-go/commit/5e82d94dea85f88a92ac88a5d449ae7da879bad6)), closes [#26](https://github.com/reyer3/bunker-go/issues/26)


### Bug Fixes

* **tui:** list the chat keys in the ? help overlay ([#20](https://github.com/reyer3/bunker-go/issues/20)) ([9c96990](https://github.com/reyer3/bunker-go/commit/9c969903ed20746184bcfec2063e127d9b293d17)), closes [#13](https://github.com/reyer3/bunker-go/issues/13)
* **tui:** redraw cleanly on resize ([#24](https://github.com/reyer3/bunker-go/issues/24)) ([dfbfb17](https://github.com/reyer3/bunker-go/commit/dfbfb1714e2daccc32afb1602069f0cba5c19063))

## [0.6.0](https://github.com/reyer3/bunker-go/compare/v0.5.0...v0.6.0) (2026-09-28)


### Features

* **tui:** attach files in chats by drag-and-drop and clipboard paste ([#11](https://github.com/reyer3/bunker-go/issues/11)) ([b7969f8](https://github.com/reyer3/bunker-go/commit/b7969f81d6502947e60b0e549ea6237fef7f7956)), closes [#5](https://github.com/reyer3/bunker-go/issues/5)
* **tui:** emoji shortcode completion in the chat composer ([#10](https://github.com/reyer3/bunker-go/issues/10)) ([2dca7c6](https://github.com/reyer3/bunker-go/commit/2dca7c6fa99508a2c8f4f1c3ad8f66f2630a5cc4)), closes [#7](https://github.com/reyer3/bunker-go/issues/7)
* **tui:** show received images inline in the chat (kitty graphics) ([#8](https://github.com/reyer3/bunker-go/issues/8)) ([a3c67bc](https://github.com/reyer3/bunker-go/commit/a3c67bc582a95659a3f5ad651b0bceb14da22ac5)), closes [#4](https://github.com/reyer3/bunker-go/issues/4)
* **tui:** video thumbnails in chat and playback with mpv ([#12](https://github.com/reyer3/bunker-go/issues/12)) ([f0ce357](https://github.com/reyer3/bunker-go/commit/f0ce3576b50cd9e6187013bd58905dac700aeece)), closes [#6](https://github.com/reyer3/bunker-go/issues/6)

## [0.5.0](https://github.com/reyer3/bunker-go/compare/v0.4.0...v0.5.0) (2026-09-28)


### Features

* **whatsapp:** opt-in voice calls; automate releases in CI ([#1](https://github.com/reyer3/bunker-go/issues/1)) ([58e3540](https://github.com/reyer3/bunker-go/commit/58e35404607949a2c1c203f1bea0437016dc04ea))
