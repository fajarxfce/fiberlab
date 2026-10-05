# Fiberlab

Lab FTTH untuk menguji incident management dan billing dari satu mesin Linux.
Editor memakai **Svelte 5 + Svelte Flow**. Backend **Go + SQLite** menyajikan UI
langsung dari satu binary. Node.js hanya diperlukan saat membangun frontend.

MikroTik menjalankan **RouterOS CHR asli di QEMU/KVM**. Jalur pelanggan memakai
Linux bridge dengan VLAN, network namespace, dan proses **pppd/PPPoE asli** per
pelanggan. OLT/ONU adalah model GPON: daya optik dan registrasi dihitung oleh
simulator, lalu diterapkan ke jalur paket Ethernet. Tidak ada emulasi firmware
OLT, laser GPON, OMCI, atau timing TDMA.

## Menjalankan

Host: Linux x86_64 dengan KVM, Go **1.26+**, Node **22.12+** untuk build,
QEMU, iproute2, pppd **2.5.x** beserta plugin PPPoE, dan ping.

Contoh dependensi runtime:

~~~sh
# Arch Linux
sudo pacman -S qemu-base iproute2 ppp iputils

# Debian/Ubuntu (pastikan pppd 2.5.x tersedia)
sudo apt install qemu-system-x86 qemu-utils iproute2 ppp iputils-ping
~~~

~~~sh
make build
./bin/ftthlab doctor
./bin/ftthlab images fetch --version 7.20.8
./bin/ftthlab serve
~~~

Buka **http://127.0.0.1:8787**. Image diunduh langsung dari MikroTik, diverifikasi
CRC arsipnya, dan dicatat SHA-256-nya. Default adalah CHR **7.20.8 long-term**.
Bisa mengimpor raw image resmi:

~~~sh
./bin/ftthlab images import --version 7.20.8 --path /path/chr-7.20.8.img
~~~

Binary `bin/ftthlab` juga bisa dibuka dengan **klik dua kali**. Browser terbuka
otomatis dan data tetap memakai `.data` di root proyek, meskipun file manager
menjalankan binary dari folder `bin`. Klik berikutnya membuka instance yang sama.
Untuk binary yang dipasang di luar proyek, default data adalah
`$XDG_DATA_HOME/fiberlab` atau `~/.local/share/fiberlab`. Opsi `--data-dir`
mengganti lokasi tersebut; path relatif pada opsi ini mengikuti direktori terminal.

Buka **Runtime settings → Start network helper**, lalu isi password akun Linux
di dialog sistem. Tombol ini memakai Polkit (`pkexec`) dan agent desktop yang
tersedia pada sesi login. Setelah status Connected, pilih image dan **Run lab**.
Helper perlu dinyalakan kembali setelah laptop reboot.

Alternatif tanpa Polkit, di terminal kedua dari direktori proyek yang sama:

~~~sh
sudo ./bin/ftthlab netd --data-dir "$PWD/.data" --uid "$(id -u)"
~~~

Helper memerlukan root untuk TAP, namespace, dan /dev/ppp. Web app berjalan sebagai
user biasa; proses QEMU juga diturunkan ke UID user tersebut. Jika modul perangkat
belum tersedia, muat modul kernel yang sesuai: kvm_intel atau kvm_amd, tun,
ppp_generic, dan pppoe. Akses /dev/kvm harus tersedia bagi user yang menjalankan
QEMU. Hasil doctor pada panel Runtime menjelaskan dependensi yang belum siap.

Pilih image pada **Runtime settings**, lalu **Run lab**. Pilih node/kabel untuk
mengubah konfigurasi atau menyisipkan gangguan. Simpan posisi otomatis,
undo/redo, pencarian, preset 8/32/100/500 ONU, serta import/export tersedia.
Perubahan struktur memerlukan lab dihentikan lebih dulu.

Hentikan lab lewat **Stop lab** sebelum menutup helper. SIGINT/SIGTERM pada helper
juga membersihkan proses dan jaringan milik lab. Setelah crash, helper berikutnya
menggunakan jurnal kepemilikan untuk memulihkan resource yang tertinggal.
Cleanup manual, ketika helper sudah berhenti:

~~~sh
sudo ./bin/ftthlab netd --data-dir "$PWD/.data" --uid "$(id -u)" --cleanup
~~~

## Menghubungkan aplikasi incident/billing

Panel **Integration** menampilkan alamat, port, community, dan kredensial lab.

| Endpoint default | Layanan |
| --- | --- |
| 10.203.0.10 | CHR: Winbox TCP 8291, API TCP 8728, SSH TCP 22, SNMP UDP 161 |
| 10.203.0.64 | OLT reference: SNMP UDP 161, SSH TCP 22, Telnet TCP 23 |
| 10.203.0.1 | RADIUS built-in: UDP 1812/1813 |
| 198.18.0.1:8080 | HTTP origin untuk tes dari namespace pelanggan |

Alamat router dan OLT berikutnya dialokasikan secara berurutan. Pool IPv4
pelanggan adalah 172.30.0.0/22. Semua endpoint aktif setelah runtime berjalan.
Gunakan IP management di atas dari aplikasi yang berjalan pada host yang sama.

Untuk **Winbox**, jalankan lab, buka **Integration**, lalu salin **Winbox address**
dan **Copy password** dari kartu MikroTik yang dipilih. Contoh Connect To adalah
`10.203.0.10:8291` dengan login `admin`. Password dibuat per router dan tersimpan
bersama lab; gunakan password kartu tersebut, bukan password Linux atau password
kosong. Akses dengan IP menghindari ketergantungan pada neighbor/MAC discovery.

Lab berbeda memakai rentang IP management yang sama dengan kredensial berbeda.
Jika lab lain sedang aktif, Integration menampilkan namanya dan tombol
**Switch to active lab**. Membuka editor saja belum menyalakan VM MikroTik.
Data `bin/.data` yang mungkin tercipta oleh binary versi lama tetap tersimpan;
buka dengan `--data-dir bin/.data` bila perlu mengekspor lab dari lokasi tersebut.

Untuk billing dengan RADIUS sendiri, pilih **Your RADIUS server** sebelum Run,
isi shared secret dan port, lalu daftarkan IP CHR sebagai NAS pada server tersebut.
Server lokal dapat bind ke **10.203.0.1** atau 0.0.0.0; 127.0.0.1 milik guest
CHR berbeda dari loopback host. Alamat server lain harus punya jalur dari CHR
melalui management bridge. Helper tidak mengaktifkan forwarding/NAT host untuk
mencapai server di mesin lain. Policy dan accounting pada mode external
dikelola aplikasi billing/server tersebut.

**Profil HSGQ-G08R dibatasi secara eksplisit.** SNMP system/IF-MIB/IF-X-MIB
tersedia, ditambah FTTHLAB-MIB di enterprise eksperimental 32473.42.
CLI menyediakan perintah "lab ..."; OID/perintah vendor HSGQ yang belum memiliki
fixture mengembalikan unsupported. Adapter HSGQ produksi perlu fixture MIB/CLI
model dan versi firmware yang cocok sebelum kompatibilitasnya bisa dijamin.
Lihat [protokol dan API](docs/protocols.md).

## Menghubungkan ONU ke ACS / GenieACS

Buka **Runtime settings → ONU ACS**, aktifkan **Connect to ACS**, lalu isi
URL CWMP server (misalnya http://127.0.0.1:7547), autentikasi dan interval Inform.
Setelah Run, setiap ONU dengan PPP aktif mendaftar memakai identitas tersendiri.
Inspector menyediakan status, Inform terakhir, serta override ACS per ONU.

TR-069/CWMP mendukung pembacaan parameter TR-098, pengaturan Inform/Wi-Fi simulasi,
connection request HTTP Digest, reboot ONU dan factory reset parameter ACS.
Kredensial connection request perlu disamakan di GenieACS; tombol copy pada
pengaturan menyiapkan ekspresi cwmp.connectionRequestAuth. Konfigurasi disimpan
di SQLite. Agent berjalan di proses Go yang sama, maksimum delapan sesi ACS
bersamaan, tanpa VM/container tambahan.

CWMP memakai jaringan laptop dan mengikuti ketersediaan PPP ONU. Trafiknya
tidak melewati CHR/namespace dan tidak muncul di PCAP feeder. Wi-Fi adalah
model konfigurasi; username/counter WAN dibaca dari PPP asli. Panduan URL
lokal/remote, parameter dan pengujian: [ACS / GenieACS](docs/acs.md).

## Gangguan dan observasi

- Putus drop/feeder, redaman tambahan, PON down, power/admin down, otorisasi ONU,
  dan mismatch VLAN mempengaruhi forwarding yang sebenarnya.
- Optical LOS berbeda dari PPPoE down. Billing suspend tidak menghasilkan LOS.
- Suspend/rate pada RADIUS built-in mengirim Disconnect-Request ke CHR;
  pelanggan login ulang memakai policy terbaru. Perubahan ditolak jika
  sesi aktif tidak bisa diputuskan.
- Timeout/reject RADIUS berlaku untuk autentikasi baru; sesi yang sudah terbentuk
  bertahan sampai terputus.
- Terminal CHR memakai SSH asli. Terminal ONU menjalankan ping, HTTP, ip addr,
  ip route, dan pembacaan log PPP dalam namespace pelanggan.
- Capture menghasilkan PCAP Ethernet; kabel optik bersama menggabungkan trafik
  endpoint pelanggan di bawahnya. Ini bukan capture frame optik GPON.

Sesi/IP/counter berasal dari interface PPP kernel dan accounting, bukan jumlah
ikon pada canvas. Perubahan tersedia lewat SSE; sampler runtime berjalan setiap
3 detik. Hilangnya helper menghasilkan status unknown. Optical power tetap
merupakan hasil model, termasuk saat menampilkan preview.

## Ringan, dengan biaya VM yang terlihat

Runtime aplikasi tidak membutuhkan Node.js, Docker, Redis, atau database server.
Terminal xterm dimuat hanya ketika dibuka. OLT tidak membuat satu VM per ONU;
yang dialokasikan adalah namespace, pasangan veth, dan pppd untuk setiap
pelanggan aktif. Log proses dibatasi; event SQLite disimpan sampai 2.000 per lab.
Panel Performance menampilkan RSS aktual aplikasi, helper, CHR, dan klien PPP.
Jumlah RSS proses menghitung halaman shared berulang; bukan ukuran PSS.

Satu CHR secara default diberi RAM guest 1 GiB. 500 PPPoE aktif mempunyai biaya
memori/CPU kernel dan pppd tambahan, sehingga angka penggunaan harus diukur
pada host target. Lisensi CHR free membatasi throughput **1 Mbps per interface**:
profil 500 cocok untuk sesi dan trafik kecil, bukan benchmark 500 Mbps.

## Verifikasi

~~~sh
make test
make check
cd web
npm run test:ui
~~~

Playwright memakai Chromium cache lokal jika tersedia. Jika belum, jalankan
"npx playwright install chromium" dari web. Build binary sebelum tes browser.

Tes integrasi opsional berikut memakai image CHR asli dan namespace terpisah,
tanpa perubahan jaringan host:

~~~sh
FIBERLAB_CHR_IMAGE="$PWD/.data/images/chr-7.20.8.img" \
  go test -v ./internal/engine -run TestCHRBootAndNativeAPI -count=1
go test -c -o bin/engine.test ./internal/engine
FIBERLAB_KERNEL_TEST=1 unshare --user --map-root-user --net \
  ./bin/engine.test -test.v -test.run TestKernelVLANFaultsAndPCAP
~~~

Tes CHR mencakup login awal/persisted overlay, konfigurasi API idempotent,
PPPoE PAP/CHAP melalui Ethernet virtual, Framed-IP, rate queue, accounting
Start/Interim/Stop, Disconnect-ACK dan reboot. Tes kernel mencakup VLAN
tagging/filtering, cut/repair dan PCAP dengan frame PPPoE discovery.

Setelah app dan helper root aktif:

~~~sh
python3 scripts/verify_runtime.py --onus 8 --seconds 60 --faults
python3 scripts/verify_runtime.py --onus 500 --seconds 1800 --faults
~~~

Harness membuat lab tes tersendiri, memeriksa sesi pada kernel **dan** API CHR,
menjalankan trafik HTTP dari pelanggan, mencatat RSS/counter, lalu menghentikan
lab. Hasil berada di artifacts/runtime-*.json dan CSV. Dukungan preset 500
bukan bukti 500 sesi sudah diuji: hasil harness-lah bukti untuk host tersebut.
Selama periode stabil, ID sesi juga diperiksa agar reconnect singkat tetap
terdeteksi. Tambahkan --keep-lab untuk menyimpan dokumen lab setelah runtime
berhenti.

Saat sebuah preset dengan satu OLT dan RADIUS built-in sedang aktif, cek juga
SNMP/SSH native menggunakan ID lab dari GET /api/v1/labs:

~~~sh
go run scripts/probe_native.go --lab-id LAB_ID
~~~

Data .data berisi topology, subscriber secrets, key kontrol dan image. Snapshot
export juga berisi kredensial agar lab dapat direproduksi. Runtime root tersimpan
di /var/lib/fiberlab/UID. Port web dibatasi loopback; helper memakai socket Unix
0600. Helper menolak subnet yang bentrok, tidak mengubah default route,
tidak flush firewall, dan hanya menghapus resource yang tercatat miliknya.

Lihat [arsitektur](docs/architecture.md) dan [catatan validasi](docs/validation.md).
