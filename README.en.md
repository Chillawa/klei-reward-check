# Klei Points Auto-Claim

[简体中文](README.md)

The app checks for free Klei Points links every hour, claims the points, and sends a desktop notification when they reach your account. Turn on **Also claim mystery gifts** to try claiming hidden rewards too.

## Getting started

1. Open **Releases** on the right side of the repository page. Under **Assets** in the latest release, download and unzip the file for your system. Choose `arm64` for Apple silicon or other ARM devices, or `x64` for Intel or AMD.
2. Open the app as shown below, click **Sign in and enable**, and sign in on Klei’s website.
3. You can close the window after signing in. The app checks in the background while your computer is on, you’re signed in to your operating system, and you’re online.

| System | How to open the app |
| --- | --- |
| macOS 13 or later | Drag the app from the ZIP to **Applications**, then double-click it. |
| Windows 10 or 11 | Double-click `klei-points-auto-claim.exe`. Requires curl and a browser (Edge, Chrome, or Chromium). |
| Linux desktop | Run `./klei-points-auto-claim` in the extracted folder. Requires curl, systemd user services, and a browser (Edge, Chrome, or Chromium). |

If you use v1.0, download and open the latest version once. From v1.1 onward, the app updates when you open it or during automatic reward checks while the window is closed. Keep it in a folder your account can write to.

The code is licensed under the [MIT License](LICENSE). The [points icon](desktop/core/ui/ICON-NOTICE.txt) belongs to Klei Entertainment.
