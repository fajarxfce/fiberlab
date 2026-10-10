# HSGQ fixture provenance

`g01id.snmp` is the supported inventory/optical subset of a read-only SNMPv2c
capture from a user-provided HSGQ-G01ID on 2026-10-10. The system response was
`HSGQ-G01ID`, sysObjectID `1.3.6.1.4.1.50224.3.1.1`. Firmware revision was not
independently identified. ONU names, indices, ASN.1 types, online/offline codes
and optical samples are retained; subscriber serials are replaced with
`HSGQ00000001`–`03`. No credentials, management address, customer names or real
serials/MACs belong in these fixtures.

The observed GPON inventory uses zero-based ONT numbers (`ONT01/000`), ASCII
12-character GPON serials in column 15 and status in column 4. Optical entries
use the inventory index followed by `.0.0`; offline ONTs lack that optical row.
The separate `.65535.65535` row at the PON index is the OLT's transceiver view,
not an ONU's optical sample. It is deliberately outside this supported subset.
RX/TX column positions and centidBm scale agree with the optical table layout
used by the verified EPON adapter and plausible GPON RX/TX values. These are
observational mappings, not a full vendor MIB certification.

`e04i.snmp` is reconstructed from the already verified examples in
`snmp/src/test/kotlin/com/duluin/ftth/snmp/HsgqEponSnmpAdapterTest.kt` and
`HsgqEponSnmpAdapter.kt` in the user's FTTH source, inspected at commit
`63d32e73`. The adapter at deployed revision
`bb6f51d5583ad26f23f60b64dfac9f9ae75172c0` uses the same OIDs. MAC values are
replaced with synthetic lab MACs. EPON IDs start at 1; inventory uses MAC column
7 and status column 8. RX/TX are Integer32 in centidBm and use `.0.0` indices.
The offline ONU remains in inventory but has no optics. **This EPON table was
not exposed by the supplied GPON G01ID.** Serving both is a simulator extension
to let the existing FTTH adapter read the same optical model without changes.

The simulator retains its nine logical IF-MIB interfaces and experimental
sysObjectID. The physical G01ID capture instead reports six interfaces:
PON01, GE01–GE04 and XGE01. Tests intentionally do not claim complete walk,
hardware, firmware, interface counter or provisioning equivalence.
