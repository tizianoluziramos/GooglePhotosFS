# GooglePhotosFS

Mount Google Photos as a drive on Windows 10 or 11 x64.

## Quick start

1. Install WinFsp Core and extract the Windows x64 ZIP. [WinFsp releases](https://github.com/winfsp/winfsp/releases/latest)
2. Run `.\googlephotosfs.exe config manual` in PowerShell. Sign in and paste the `oauth_token` cookie from your browser's developer tools. [Google sign-in](https://accounts.google.com/EmbeddedSetup)
3. Run `.\googlephotosfs.exe mount` and open `P:` in File Explorer.
4. Run `.\googlephotosfs.exe unmount` to disconnect.

## Build

With Go 1.25 or later, run `.\build.ps1`. The executable is in `dist/`.
