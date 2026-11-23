# Install on Mac

Open the disk image, start OwnShop AI once, and the shop page opens in your browser. For Macs with Apple silicon (M1 or later).

> Not yet tested on a real customer Mac. The steps below follow how the Mac app is built. The Gatekeeper screens differ between macOS versions; if yours look different, follow the closest match and tell us.

## What you need

- A Mac with Apple silicon (M1, M2, M3 or later). Intel Macs are not supported.
- About 1.5 GB of free disk space. The download is about 690 MB because the AI model is inside it.
- An internet connection for WhatsApp and Telegram.

## 1. Download and open the disk image

1. Download `ownshop-macos-arm64.dmg` from the [download section](/#download).
2. Double-click it. A window opens with **OwnShop AI** and an **Applications** folder.
3. Drag **OwnShop AI** onto **Applications** if you want it there. You can also open it straight from the disk image: the first time it starts from the disk image it copies itself to the **Applications** folder inside your home folder (`~/Applications`) and runs from there, so it keeps working after you eject the disk image.

## 2. Open it the first time

The Mac app is not notarized by Apple yet, so macOS asks before the first start.

1. Double-click **OwnShop AI**.
2. If macOS says it cannot check the app for malicious software, click **Done** or **Cancel** (not Move to Trash).
3. Open **System Settings > Privacy & Security**, scroll down, and click **Open Anyway** next to the message about OwnShop AI. Confirm with your password if asked.
4. On older macOS versions you can instead right-click (or Control-click) **OwnShop AI**, choose **Open**, then **Open** again.
5. The shop page opens in your browser at `http://localhost:2007` with the first setup step.

You only do this once. Next, go to [First setup](/manual/first-setup/).

## What it did

- Your shop's data lives in `~/Library/Application Support/ownshop`.
- An **OwnShop AI** link to the shop page was put on your desktop.
- OwnShop AI was set to start in the background when you log in (a login item named `com.deemwar.ownshop`).

## Open, start and stop

- **Open the shop page:** double-click the **OwnShop AI** link on the desktop, or open `http://localhost:2007`. Opening the app while it already runs just opens the page.
- **Start it** after you stopped it: open **OwnShop AI** from Applications.
- **Stop it:** there is no Quit button. Open **Activity Monitor**, find **ownshop**, and quit it.

## If it goes wrong

- **No Open Anyway button.** It appears only after you tried to open the app once. Try step 1 again, then go back to Privacy & Security.
- **The page does not load.** Start the app again. If port 2007 is in use, the shop moves to 2008 or the next free port; the desktop link follows it.
- **"OwnShop AI is damaged".** The download may be incomplete. Delete it and download it again.
