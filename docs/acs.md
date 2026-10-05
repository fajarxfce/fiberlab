# Menghubungkan ONU ke GenieACS

Fiberlab menyediakan klien **TR-069 / CWMP 1.0** per ONU dengan parameter
**TR-098** di bawah InternetGatewayDevice. Setiap ONU melakukan HTTP/SOAP
ke ACS yang dipilih. Tidak perlu menjalankan GenieACS, MongoDB, Node.js,
container, atau VM tambahan sebagai bagian dari aplikasi Fiberlab.

## Pengaturan

1. Buka **Runtime settings → ONU ACS → Connect to ACS**.
2. Isi **ACS URL**, misalnya http://127.0.0.1:7547 untuk GenieACS di laptop
   yang sama, atau https://acs.example.net untuk server lain. Isi ACS username
   dan password jika server memakai autentikasi. Basic dan Digest didukung.
3. Tentukan **Periodic Inform**, antara 10 dan 86.400 detik. Default 60 detik.
4. Atur connection request seperti tabel berikut, kemudian **Save ACS settings**.
5. Jalankan lab. ONU akan muncul di **Devices** GenieACS setelah PPP tersambung.

| Posisi GenieACS | Listen address di Fiberlab | Public URL di Fiberlab |
| --- | --- | --- |
| Laptop yang sama | 127.0.0.1:7548 | http://127.0.0.1:7548 |
| Mesin lain, contoh IP laptop 192.168.1.20 | 0.0.0.0:7548 | http://192.168.1.20:7548 |
| Di belakang reverse proxy | IP/port lokal tempat proxy meneruskan request | URL HTTP/HTTPS yang dapat dijangkau ACS |

Public URL harus mengarah ke laptop ini dari sisi ACS. Jika GenieACS berjalan
di container, 127.0.0.1 di container menunjuk ke container itu sendiri; gunakan
alamat host yang dapat dijangkau dari container. Listener menerima HTTP;
HTTPS connection request dapat diterminasi oleh reverse proxy. ACS URL outbound
mendukung HTTPS dengan verifikasi sertifikat melalui trust store sistem.

Autentikasi ke ACS dan autentikasi connection request adalah dua pasang
kredensial yang berbeda. Untuk autentikasi Inform, GenieACS dapat memakai
konfigurasi **cwmp.auth**, misalnya:

~~~text
AUTH("cpe-user", "cpe-password")
~~~

Untuk menjalankan task seketika, samakan kredensial connection request di
GenieACS melalui **Admin → Config → cwmp.connectionRequestAuth**. Tombol
**Copy GenieACS connection auth** menyalin ekspresi AUTH dengan nilai dari
pengaturan Fiberlab. Alternatifnya, provision parameter ConnectionRequestUsername
dan ConnectionRequestPassword dari GenieACS. Parameter password dibaca sebagai
string kosong sesuai TR-098, sehingga ACS perlu mengetahui password yang diset.

Port connection request hanya melayani permintaan CWMP yang diautentikasi dengan
HTTP Digest. UI dan API kontrol Fiberlab tetap berada di loopback port 8787.

## Identitas, status dan override

Setiap perangkat memiliki Manufacturer Fiberlab, OUI reference 024654,
ProductClass FiberlabONU, serta SerialNumber berupa serial GPON diikuti ID lab.
Serial tetap sama setelah Stop/Run dan restart aplikasi. Lab yang berbeda
mendapat identitas berbeda meskipun memakai serial GPON preset yang sama.
GenieACS melakukan percent-encoding pada tanda hubung di bagian serial; ID
yang ditampilkan inspector sudah mengikuti format tersebut.

Pilih ONU untuk melihat status ACS, waktu Inform terakhir, jumlah Inform,
dan ID GenieACS. **Send Inform now** menjadwalkan sesi baru untuk ONU tersebut.
Di tab **Configure**, ONU dapat dinonaktifkan dari ACS, diarahkan ke URL ACS
lain, atau diberi interval Inform sendiri. URL kosong memakai pengaturan lab;
jika URL override diisi, username/password override juga digunakan. Interval
0 berarti mengikuti lab. Perubahan pengaturan di UI dilakukan ketika lab berhenti.

Power off/on ONU juga menghasilkan BOOT dan mengulang uptime perangkat saat
kembali hidup. Pemulihan kabel hanya melaporkan perubahan konektivitas, sehingga
tidak membuat reboot palsu di ACS.

Status online berarti ACS telah mengakui Inform dan sesi CWMP selesai.
Error autentikasi/HTTP ditampilkan di inspector dan Activity. Retry memakai
backoff 5–300 detik. Jika ACS tidak bisa dihubungi, ONU tidak dilaporkan
berhasil terdaftar. Inform yang sudah diakui tetap memiliki waktu Last Inform
meskipun RPC berikutnya dalam sesi mengalami error.

## RPC dan parameter

| RPC | Perilaku |
| --- | --- |
| Inform | BOOTSTRAP, BOOT, PERIODIC, CONNECTION REQUEST dan perubahan konektivitas |
| GetRPCMethods | Mengembalikan daftar RPC yang benar-benar diimplementasikan |
| GetParameterNames | Penelusuran objek/parameter, termasuk NextLevel |
| GetParameterValues | Identitas, konfigurasi dan observasi PPP/optik |
| SetParameterValues | Validasi tipe/nilai, perubahan atomik, ParameterKey, penyimpanan SQLite |
| Reboot | Menghapus sesi PPP ONU melalui API CHR dan mematikan jalur selama tiga detik; pelanggan lain tetap tersambung |
| FactoryReset | Menghapus parameter hasil provisioning ACS, kembali ke default lab, lalu reboot ONU |

Reboot dan factory reset hanya menyasar ONU yang menerima RPC. Factory reset
tidak menghapus lab, akun billing, konfigurasi CHR, atau data laptop. Konfigurasi
yang ditulis ACS disimpan terpisah dari revisi editor dan bertahan setelah
reboot serta Stop/Run. Snapshot topology mengekspor pengaturan koneksi ACS;
hasil provisioning ACS tersimpan di SQLite lokal dan tidak disalin ke identitas
baru ketika snapshot diimpor.

| Path setelah InternetGatewayDevice. | Dukungan |
| --- | --- |
| DeviceInfo.* | Identitas dan uptime; ProvisioningCode dapat ditulis |
| ManagementServer.URL / Username / Password | Dapat ditulis; digunakan pada sesi berikutnya |
| ManagementServer.PeriodicInformEnable / PeriodicInformInterval | Dapat ditulis |
| ManagementServer.ConnectionRequestUsername / ConnectionRequestPassword | Dapat ditulis |
| ManagementServer.ConnectionRequestURL / ParameterKey | Baca saja |
| LANDevice.1.WLANConfiguration.1.Enable / SSID / KeyPassphrase | Konfigurasi Wi-Fi simulasi, dapat ditulis |
| LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase | Alias passphrase Wi-Fi |
| WANDevice.1.WANConnectionDevice.1.WANPPPConnection.1.* | Baca saja; IP/status/uptime/counter berasal dari PPP asli |
| X_FIBERLAB_GPONSerial / OLT / PON / RXPower | Identitas GPON dan telemetry model optik |

Parameter password selalu mengembalikan string kosong ketika dibaca. Counter
EthernetBytesReceived/Sent TR-098 adalah unsignedInt 32-bit dan mengikuti wrap
32-bit; API runtime Fiberlab menyediakan counter 64-bit. Username PPP dibaca
dari akun lab; pengaturan akun/policy tetap melalui lab atau RADIUS/billing.

RPC lain, termasuk Download firmware, AddObject/DeleteObject, dan
Get/SetParameterAttributes, mengembalikan fault 9000. Parameter tidak dikenal
mengembalikan 9005, tipe salah 9006, nilai tidak valid 9007, dan parameter baca
saja 9008. Ini profil reference; tidak mengklaim seluruh TR-098/TR-181 atau
parameter vendor tertentu. Wi-Fi menyimpan konfigurasi tanpa memancarkan radio.

## Jalur jaringan dan batas resource

CWMP menggunakan **koneksi host laptop**, dengan satu scheduler Go dan maksimum
delapan sesi HTTP aktif bersamaan. Setiap sesi memiliki cookie dan koneksi TCP
sendiri. ONU idle tidak mendapat proses, container, atau goroutine khusus ACS.
Sesi dibatasi 90 detik, 128 RPC, serta body SOAP 2 MiB.

Sesi ACS hanya berjalan ketika runtime aktif, jalur optik/service tersedia,
dan PPP ONU benar-benar connected. Kabel/PON/power down dan billing suspend
menghentikan koneksi ACS pada pembaruan runtime berikutnya. Gangguan tidak
membuat Inform sukses palsu; setelah pemulihan agent menghubungi ACS kembali.
Profil ini memakai ketersediaan layanan PPP yang sama, belum memiliki VLAN
management ACS independen dari billing.

Trafik CWMP tidak melewati PPP namespace/CHR dan tidak termasuk PCAP feeder
atau counter PPP. IP WAN yang dilaporkan tetap IP PPP ONU, sementara source IP
koneksi ACS adalah IP laptop. Ini memungkinkan pengujian ACS eksternal tanpa
mengubah NAT, firewall, atau default route host.

## Mengulang tes dengan GenieACS asli

Fixture berikut hanya untuk validasi lokal, terpisah dari server ACS pengguna.
Ia mengunduh MongoDB resmi dan menjalankan database sementara di loopback.

~~~sh
npm install --prefix artifacts/acs-tools --no-audit --no-fund --ignore-scripts \
  genieacs@1.2.16 mongodb@4 mongodb-memory-server-core@10
node scripts/acs_fixture.cjs
~~~

Di terminal lain, dengan aplikasi dan helper sudah berjalan:

~~~sh
python3 scripts/verify_acs.py --keep-lab
~~~

Harness membuat tiga pelanggan PPP asli: dua dikelola ACS dan satu opt-out.
Tes memeriksa penolakan password salah, identitas, penelusuran parameter,
connection request berulang, SetParameterValues, periodic Inform, reboot
dengan ID sesi PPP baru, cut/repair, persistence setelah restart, factory reset,
dan cleanup. Laporan berada di artifacts/acs-genieacs-*.json.
Hentikan fixture dengan Ctrl+C setelah tes; MongoDB dan worker GenieACS ikut berhenti.

Untuk GenieACS NBI, task refreshObject menggunakan objectName tanpa titik
di ujung, misalnya InternetGatewayDevice atau
InternetGatewayDevice.LANDevice.1.WLANConfiguration.1. Nama parameter dalam
SOAP CWMP tetap mengikuti aturan path TR-098.
