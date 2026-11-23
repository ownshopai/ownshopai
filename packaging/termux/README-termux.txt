OwnShop AI on Android (Termux)

One line, in Termux:  curl -fsSL https://downloads.deemwar.com/ownshopai/termux/install-termux.sh | sh
After that, type `ownshop` in a new Termux session to start it again.

For learning; for your business, deemwar.com

Needs: an arm64 phone from 2018 or newer (ARMv8.2) with 4 GB RAM or more, about 2 GB free storage.

1. Install Termux from F-Droid (not the Play Store version). Optional: the Termux:API app
   and `pkg install termux-api` (wake lock, opens the browser).
2. In Termux:
     tar -xzf ownshop-linux-arm64.tar.gz
     cd OwnShop AI
     ./run-termux.sh
3. The shop page opens (or open http://127.0.0.1:2007 in the phone's browser).

Android may stop Termux: set Termux battery to Unrestricted.

Stop: ./run-termux.sh stop    Your shop files are in ~/.config/ownshop.
