# Install on Windows

Download the right setup file, run it, and the shop page opens in your browser. No wizard, no account.

## What you need

- A Windows 10 or 11 computer that can stay on while you want the shop to answer.
- About 1.5 GB of free disk space. The download is about 700 MB because the AI model is inside it.
- An internet connection for WhatsApp and Telegram. The AI itself runs on your computer.

## 1. Pick the right file

There are two Windows files. They are the same program for two kinds of processor.

| Your computer | File to download |
|---|---|
| Intel or AMD processor (most PCs) | `ownshop-setup-windows-amd64.exe` (x64) |
| Snapdragon or other ARM processor | `ownshop-setup-windows-arm64.exe` (ARM64) |

Not sure? Open **Settings > System > About** and look at **System type**. "x64-based processor" means the x64 file; "ARM-based processor" means the ARM64 file.

Both files are on the [download section](/#download) of this site.

## 2. Run the setup file

1. Open your **Downloads** folder and double-click the setup file.
2. If Windows shows **Windows protected your PC**, click **More info**, then **Run anyway**. Windows shows this for programs it has not seen often yet. It does not appear on every computer.
3. Wait. There is no wizard and nothing to click. The setup copies about 700 MB, so this can take a minute on a slow disk.
4. The shop page opens in your browser at `http://localhost:2007` with the first setup step, **Your shop**.

What you should see next is [First setup](/manual/first-setup/).

## What the setup did

- The program and the AI model went into `%LOCALAPPDATA%\ownshop` (for example `C:\Users\<you>\AppData\Local\ownshop`).
- Your shop's own data (database, products file, photos, orders file, backups) lives in `%APPDATA%\ownshop`. You will see this folder named on the Products and Orders pages.
- **OwnShop AI** was added to the Start menu. It starts the shop.
- An **OwnShop AI** shortcut was put on the desktop. It opens the shop page in your browser.
- OwnShop AI was set to start by itself when you log in to Windows (a small `ownshop.cmd` in your Startup folder). It starts in the background, without opening the browser.

## Open, start and stop

- **Open the shop page:** double-click **OwnShop AI** on the desktop, or open `http://localhost:2007` in any browser on this computer. Double-clicking the Start menu entry while the shop already runs just opens the page; it does not start a second copy.
- **Start the shop** after you stopped it: Start menu > **OwnShop AI**.
- **Stop the shop:** there is no Stop button in the app. Open **Task Manager**, find **ownshop.exe** under Details, and click **End task**. While it is stopped, nobody gets a reply.
- **Start at login** cannot be switched off from the shop page yet. The shop writes its startup entry again every time it starts.

> If port 2007 is already used by another program, OwnShop AI takes the next free one (2008, 2009 and so on) and remembers it. The desktop shortcut is updated to match, so use the shortcut if `localhost:2007` does not open.

## Install a new version

Download the new setup file and run it the same way. It stops the running shop, replaces the program and keeps your shop's data in `%APPDATA%\ownshop`.

## If it goes wrong

- **"This setup file is incomplete (download it again)".** The download was cut off. Delete the file and download it again.
- **The browser did not open.** Double-click the **OwnShop AI** shortcut on the desktop, or type `http://localhost:2007` in the browser.
- **The page does not load.** The shop may not be running. Start it from the Start menu. If another program uses port 2007, try `http://localhost:2008`.
- **Windows blocked the file and there is no Run anyway.** Your computer may be managed by a company policy that only allows approved programs. Ask whoever manages the computer.

To remove the program later, see [Troubleshooting and FAQ](/manual/troubleshooting/#uninstall).
