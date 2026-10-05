# Catatan validasi

Validasi lokal pada **4 Oktober 2026**, Linux x86_64 (Arch), Intel i7-10870H
16 logical CPU, RAM sekitar 15 GiB. Toolchain: Go 1.26.3, Node 24.15,
QEMU 11.1.1, pppd 2.5.3 dan Chromium dari cache Playwright.

**Uji end-to-end 8 ONU dan 500 sesi pppd bersamaan selama 30 menit sudah lulus.**
Tes 500 menyelesaikan 15.000 probe HTTP, tanpa pergantian ID sesi. Jumlah sesi diperiksa
melalui interface PPP kernel dan API RouterOS asli; hasil preset/editor tetap
dipisahkan dari hasil jaringan.

**Dukungan ACS juga sudah diuji dengan GenieACS 1.2.16 asli.** Tiga pelanggan
PPPoE berjalan, dua memakai ACS dan satu opt-out. Inform, parameter, autentikasi,
reboot, power cycle, gangguan/pemulihan, persistence dan factory reset lulus dalam 106 detik.

Halaman ini menyimpan ringkasan hasil di repository. Laporan mentah, screenshot,
dan capture tersedia secara lokal di folder `artifacts/` yang diabaikan Git.
Harness di bawah menghasilkan laporan baru saat validasi dijalankan ulang.

## Hasil

| Pemeriksaan | Hasil | Cakupan |
| --- | --- | --- |
| go test -race ./... | Lulus | Model optik/topologi, SQLite, API, image, helper, protokol, CWMP dan pembatasan target reboot |
| go vet ./... | Lulus | Pemeriksaan statis Go |
| npm run check | Lulus, 0 error / 0 warning | Svelte dan TypeScript |
| Build frontend + binary Go | Lulus | Aset frontend tertanam dalam binary |
| Playwright | 11 skenario tervalidasi | 9 skenario editor sebelumnya serta 2 skenario ACS; tes ACS dijalankan ulang setelah perbaikan dan lulus 6,6 detik |
| Workspace utama pada lebar 1280/1536/1920 px | Lulus | Seluruh 14 perangkat berada dalam canvas, tanpa error JavaScript |
| TestKernelVLANFaultsAndPCAP | Lulus, 2,13 detik | Frame PPPoE discovery nyata, VLAN tagging/filtering, cut/repair, capture PCAP |
| TestCHRBootAndNativeAPI | Lulus, 33,30 detik | Boot CHR asli, login awal/persisten, konfigurasi idempotent, API native, PPPoE/RADIUS, QMP |
| FTTHLAB-MIB dengan snmptranslate | Lulus | MIB SMIv2 dapat dimuat dan OID ONU status diterjemahkan |
| Laporan harness dengan input sintetis | Lulus | Hasil tetap disimpan saat berhasil, gagal menghapus dokumen, dan dibatalkan; bukan tes jaringan |
| Harness runtime 8 ONU | Lulus | 8/8 sesi selama 60 detik, HTTP dari semua ONU, gangguan/pemulihan dan cleanup |
| Harness runtime 500 ONU / 30 menit | Lulus | 500/500 sesi pada 360 sampel, 15.000 HTTP, ID sesi tetap; gangguan kabel, redaman, PON, VLAN dan billing pulih |
| Cleanup runtime 500 ONU | Lulus | 509 interface host, 500 pppd dan CHR dibersihkan; default route tetap, lab pengguna tersimpan |
| OLT native pada runtime 500 ONU | Lulus | SNMPv2c: 500 ID/IP; SSH: 500 ONU; perintah vendor tanpa fixture mengembalikan UNSUPPORTED |
| Rate queue RouterOS pada runtime 500 ONU | Lulus | 500 queue dinamis, masing-masing menargetkan interface PPPoE yang benar dengan batas 1M/1M |
| Capture feeder pada runtime 500 ONU | Lulus | 891 frame, termasuk 558 frame sesi PPPoE; hanya 63 ONU di cabang yang dipilih |
| Terminal web pada runtime 500 ONU | Lulus | SSH CHR asli, berpindah ke SSH OLT, lalu HTTP dari terminal ONU; tanpa error JavaScript |
| UI live 500 ONU selama 10 menit | Lulus | 518 perangkat dan 500 baris pelanggan aktif, filter/pilih berulang; tanpa error JavaScript |
| GenieACS native / 3 PPP clients | Lulus | 2 ONU dikelola, 1 opt-out; auth salah ditolak, Inform, GPN/GPV/SPV, callback berulang, reboot, cut/repair, restart, factory reset |
| Batas scheduler CWMP | Lulus, unit test | 500 state ONU dengan mock ACS; maksimal 8 request bersamaan, request dibatalkan ketika PPP offline |
| Cleanup sesudah ACS | Lulus | 0 interface lab / pppd / CHR tersisa, default route tetap, 15 perangkat lab pengguna tersimpan |

Tes kernel berjalan di user/network namespace terpisah dengan unshare;
bridge, TAP dan veth host utama tidak diubah. Tes ini mengirim frame Ethernet
nyata, tetapi tidak menjalankan 500 klien pppd.

Tes CHR menjalankan image resmi RouterOS **7.20.8 long-term** di QEMU/KVM.
NIC virtual terhubung melalui hub QEMU: satu klien PPPoE RouterOS melakukan
autentikasi PAP dan CHAP ke server PPPoE RouterOS dengan RADIUS built-in.
Tes memeriksa Framed-IP, queue rate 1M/1M, Accounting Start/Interim/Stop,
Disconnect-ACK, serta sesi native yang benar-benar terputus. Ini membuktikan
interoperabilitas dengan CHR, bukan keseluruhan runtime namespace per ONU.

Image yang digunakan:

~~~text
URL: https://download.mikrotik.com/routeros/7.20.8/chr-7.20.8.img.zip
Raw bytes: 134217728
SHA-256: cddbdca2a476a7fd9e6bd3390679e9e3792548c83683e912932309ca58953dd0
~~~

Tes protokol juga memakai socket UDP/TCP nyata untuk SNMPv2c, PAP/CHAP,
accounting 64-bit, Disconnect, SSH dan Telnet. Hasil tersebut tidak menjamin
kompatibilitas MIB/CLI proprietary HSGQ; profil vendor itu tetap dibatasi
seperti dijelaskan di [protokol](protocols.md).

## Pengukuran ringan

Pengukuran berikut adalah satu sampel lokal, bukan batas penggunaan maksimum.

| Pengukuran | Nilai | Kondisi |
| --- | --- | --- |
| Binary Go dengan aset UI dan CWMP | 14.598.409 byte (13,92 MiB) | Build dengan -trimpath -ldflags="-s -w", tanpa source map produksi |
| RSS aplikasi Go setelah tes ACS | 28.422.144 byte (27,11 MiB) | Runtime berhenti, lab tes tetap tersimpan, tanpa memaksa Go GC |
| Buat preset 500, filter dan pilih ONU terakhir | 1.974 ms | Browser lokal; tidak ada sesi PPP aktif |
| Heap JavaScript pada tes 500 ONU | 23.306.508 byte (22,23 MiB) | Heap JS saja, tidak termasuk seluruh proses Chromium |

Artefak lokal browser:

- artifacts/workspace.png
- artifacts/workspace-500.png
- artifacts/fiberlab-ready.png
- artifacts/ui-performance.json
- artifacts/build-metrics.json
- artifacts/build-metrics-acs.json
- artifacts/acs-settings.png

Saat runtime 500 ONU aktif, UI juga diuji selama 616 detik dengan 518 perangkat
dan 500 baris pelanggan ditampilkan. Dari 11 sampel, heap JavaScript setelah GC
berada sekitar 62–73 MiB; median filter/pilih setelah pemanasan adalah sekitar
0,5 detik. Angka ini hanya heap JS dan mencakup overhead otomasi pada pengukuran
interaksi. Rincian: artifacts/live-ui-500-summary.json dan live-ui-500.jsonl.

Angka aplikasi/editor tersebut tidak mencakup RAM CHR, kernel namespace,
bridge atau pppd. Panel Performance mengambil RSS proses sebenarnya saat
runtime hidup. Jumlah RSS menghitung shared pages berulang dan bukan PSS.
Lisensi CHR free membatasi throughput 1 Mbps per interface; tes 500 sesi
memakai trafik kecil, bukan benchmark bandwidth agregat.

## Menjalankan ulang validasi end-to-end

Jalankan app dan helper seperti di [README](../README.md), kemudian:

~~~sh
python3 scripts/verify_runtime.py --onus 8 --seconds 60 --faults
python3 scripts/verify_runtime.py --onus 500 --seconds 1800 --faults
~~~

Saat sebuah lab dengan satu OLT berjalan dan semua pelanggan sudah tersambung,
protokol management dapat diperiksa terpisah:

~~~sh
go run scripts/probe_native.go --lab-id LAB_ID --output artifacts/native-management.json
~~~

Probe tersebut membandingkan ID/IP SNMP dengan akun lab, membaca daftar ONU
melalui SSH native, dan memeriksa respons UNSUPPORTED untuk perintah vendor
tanpa fixture. Probe tidak mengubah konfigurasi perangkat.

Harness membuat lab tersendiri. Setelah semua namespace memperoleh interface
PPP dan IP, harness mencocokkan jumlah/alamat sesi dengan API RouterOS asli
dan menunggu accounting Interim. Dengan --faults, harness menguji drop,
feeder, redaman, PON, mismatch VLAN serta billing suspend/resume. Pelanggan
di luar cabang gangguan harus tetap mempertahankan sesi.

Selama periode stabil, harness menjalankan HTTP dari setiap pelanggan,
memeriksa jumlah sesi kernel dan CHR, serta menyimpan sampel RSS dan counter
ke JSON/CSV. ID sesi harus tetap sama selama periode stabil; perubahan ID
menandakan reconnect dan menggagalkan tes. Counter harus bertambah dan setiap pelanggan harus menyelesaikan
setidaknya satu probe. Runtime selalu dihentikan setelah tes. Dokumen lab
yang berhasil diuji dihapus, kecuali --keep-lab dipilih; dokumen tes gagal
disimpan untuk pemeriksaan.

Hasil 8 ONU: artifacts/runtime-8-20261004-204415.json dan CSV pasangannya.
Semua gangguan/pemulihan lulus, 8 pelanggan berhasil HTTP, dan runtime berhenti
dengan bersih. Puncak jumlah RSS empat kelompok proses adalah 322.654.208 byte
(307,71 MiB), termasuk halaman shared yang dihitung berulang.

Artefak protokol/capture pada runtime 500 ONU:

- artifacts/native-management-500.json
- artifacts/native-queues-500.json
- artifacts/runtime-500-feeder.pcap
- artifacts/pcap-500-summary.json
- artifacts/live-terminals-500.json

Hasil final lokal: `artifacts/runtime-500-20261004-204849.json`,
360 sampel di `artifacts/runtime-500-20261004-204849.csv`, dan
pemeriksaan cleanup di `artifacts/runtime-500-cleanup.json`.
Periode stabil berlangsung 1.800,000 detik dengan minimal 500 sesi aktif.
Seluruh 500 pelanggan menyelesaikan HTTP dan counter bertambah. Puncak jumlah
RSS aplikasi, helper, CHR dan 500 pppd adalah 4.881.301.504 byte (sekitar 4,55 GiB).
Ini menghitung shared pages berulang, bukan pemakaian RAM fisik unik/PSS.
Sebelum cleanup, 500 interface PPP unik juga diverifikasi melalui tampilan
jaringan /proc masing-masing pppd. Dokumen lab tes disimpan dalam keadaan berhenti.

Preflight lama artifacts/runtime-8-20261004-202031.json berhenti sebelum helper
tersedia dan bukan hasil tes jaringan yang lulus. Hasil 30 menit di atas diambil
sebelum penambahan dukungan ACS; pengujian ACS dicatat terpisah.

## Validasi ACS / GenieACS

Laporan lokal `artifacts/acs-genieacs-20261004-215426.json` memakai
GenieACS 1.2.16 dan MongoDB 7.0.16 sementara, keduanya hanya bind loopback.
Harness membuat lab terpisah dengan tiga namespace/pppd asli. Dua ONU mengirim
CWMP ke fixture dan satu dinonaktifkan dari ACS. Tidak ada server ACS pengguna
yang dihubungi atau diubah.

Tes memverifikasi kredensial salah lalu pemulihannya, ID GenieACS unik,
GetParameterNames/GetParameterValues, IP WAN yang sama dengan PPP kernel,
connection request HTTP Digest berulang, penulisan SSID/enable/passphrase,
password yang dibaca kosong, serta periodic Inform. Reboot mengganti ID sesi
PPP hanya pada ONU sasaran; pelanggan lain mempertahankan sesinya. Kabel putus
menghentikan Inform, repair memulihkannya tanpa BOOT palsu. Power off/on
menghasilkan BOOT baru di GenieACS. Setelah Stop/Run, identitas dan
konfigurasi hasil provisioning tetap tersimpan. Factory reset mengembalikan
parameter default dan melakukan reboot ONU. Cleanup akhir juga diverifikasi
terpisah di `artifacts/acs-cleanup.json`.

Pengujian awal menemukan dua bug yang sudah diperbaiki: nonce connection request
yang menghambat task kedua dan reboot singkat yang dapat mempertahankan sesi PPP.
Reboot sekarang menghapus sesi yang cocok dengan username/IP/MAC melalui API
CHR sebelum power cycle. Unit test memeriksa pembatasan target dan memastikan
reboot gagal tidak menghasilkan BOOT palsu. Laporan percobaan ACS lebih awal
adalah hasil sebelum perbaikan; gunakan laporan lulus yang disebutkan di atas.

Profil, batas protokol, jalur host untuk trafik CWMP, serta cara menjalankan
fixture/harness tersedia di [panduan ACS](acs.md). Hasil ini membuktikan dua
agent CWMP pada jaringan nyata dan batas concurrency melalui unit test;
pengukuran 500 PPP selama 30 menit di atas dilakukan dengan ACS nonaktif.
