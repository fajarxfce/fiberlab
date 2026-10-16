# Traffic download / upload ONU

Buka **Traffic** di panel bawah, pilih satu ONU atau semua ONU, isi target
download/upload per ONU, pilih durasi, lalu **Start traffic**. Lab harus running.
Nilai `0` menonaktifkan salah satu arah; **Until stopped** berjalan sampai
**Stop traffic** atau lab dihentikan. Untuk 8 ONU pada CHR free, mulai dari
64 Kbps download dan 16 Kbps upload per ONU.

Paket TCP benar-benar melewati ONU → PPPoE → OLT → MikroTik ether2 → server WAN
lab `198.18.0.1:8081`. Download berjalan dalam arah sebaliknya. Server tujuan
berada di lab, jadi tidak memerlukan Internet publik, DNS, default route, atau
`FIBERLAB_INTERNET=1`. NAT pelanggan bawaan CHR tetap digunakan. Traffic ini
menambah counter asli RouterOS dan accounting seperti traffic Internet biasa.

## Membaca hasil di aplikasi FTTH

Panel menampilkan kecepatan hasil selisih counter kernel PPP setiap 3 detik;
**Session bytes** adalah counter kumulatif sesi, bukan hanya traffic generator.
TCP ACK dan header ikut menambah byte interface, sehingga angka aktual berbeda
sedikit dari target payload.

| Arah dari pelanggan | ONU PPP | MikroTik interface PPPoE | RADIUS accounting |
| --- | --- | --- | --- |
| Download | RX | TX | Acct-Output-Octets + Acct-Output-Gigawords |
| Upload | TX | RX | Acct-Input-Octets + Acct-Input-Gigawords |

Di MikroTik, periksa interface dinamis `<pppoe-USERNAME>` melalui WinBox,
`/interface print stats`, atau IF-MIB SNMP. Queue, batas lisensi, dan gangguan
jalur tetap berlaku. CHR free membatasi **1 Mbps per interface**, dibagi oleh
sesi yang menggunakan interface tersebut; target generator bukan jaminan hasil.

Grafik pelanggan pada modul BNG FTTH menggunakan selisih counter RADIUS antar
sampel. Jika lab memakai **built-in RADIUS**, accounting diterima Fiberlab.
Untuk mengirimnya ke aplikasi FTTH, gunakan konfigurasi **external RADIUS**
dengan NAS, shared secret, dan akun pelanggan yang sesuai di FTTH. Fitur traffic
tidak mengganti konfigurasi RADIUS tersebut. Tunggu minimal dua sampel
accounting dan interval agregasi grafik; mengubah target tidak langsung
menghasilkan titik grafik baru di FTTH.

Ketika kabel terputus atau sesi PPP terputus, paket tidak melewati jalur itu.
Generator menunggu dan mencoba koneksi kembali setelah jalur pulih. Tidak ada
counter yang dinaikkan secara sintetis. Setelah PPP reconnect, counter interface
bisa kembali ke nol; sampel laju pertama/reset tidak dihitung sebagai lonjakan.

## API

Mulai traffic pada ONU tertentu (array dapat berisi sampai 500 ONU):

```sh
curl -X POST http://127.0.0.1:8787/api/v1/labs/LAB_ID/traffic \
  -H 'Content-Type: application/json' \
  -d '{"onuIds":["onu-0001","onu-0002"],"downloadKbps":64,"uploadKbps":16,"durationSeconds":300}'
```

Hentikan generator tanpa menghentikan sesi PPPoE:

```sh
curl -X DELETE http://127.0.0.1:8787/api/v1/labs/LAB_ID/traffic
```

Di mini PC, gunakan port localhost **18787**. Status job berada dalam `traffic`
pada respons runtime dan SSE. Field sesi `rxBitsPerSecond` dan
`txBitsPerSecond` adalah bit/detik dari sudut pandang ONU.

Durasi menerima 1–86400 detik, atau `0` untuk sampai dihentikan. Total target
download + upload semua ONU dibatasi 100.000 Kbps. Satu job dapat berjalan per
lab aktif; hentikan sebelum mengganti parameternya. Memilih lab lain tidak
menghentikan job, tetapi stop/restart runtime menghentikannya. Job tidak
di-autostart setelah restart dan tidak mengubah topology/export JSON.

Generator dan server berjalan dalam helper Go, memakai satu koneksi TCP per
ONU dengan buffer terbatas. Tidak memerlukan iperf, browser per ONU, proses
tambahan, file payload, atau layanan luar. Hanya pembuatan socket yang masuk
namespace ONU; goroutine lain dan routing host tidak dipindahkan.

## Verifikasi native

Dengan lab running dan tidak ada job traffic lain, jalankan:

```sh
python3 scripts/verify_traffic.py --lab-id LAB_ID --seconds 20
```

Probe membaca counter interface PPPoE langsung dari RouterOS API, menjalankan
traffic pada semua ONU, membandingkan download/upload, lalu menghentikan
generator sambil membiarkan lab running. Kredensial diambil dari API lokal dan
tidak ditulis ke laporan. Untuk mini PC gunakan `--url http://127.0.0.1:18787`.

Opsi `--radius` juga membaca paket Accounting-Request pada `flab-mgmt` dan
memastikan dua Interim-Update setiap pelanggan memiliki counter naik. Jalankan
di network namespace helper dengan izin raw socket. Probe menunggu sedikitnya
dua interval accounting (sekitar 130 detik jika interval 60 detik), menyimpan
hanya counter dan ID sesi, lalu menutup capture. Opsi ini ditujukan untuk lab
dengan built-in RADIUS; tidak mengubah konfigurasi RADIUS atau NAS.
