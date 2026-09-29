# Passive AV devices

Declarative packages for the boxes that sit in a signal path and have nothing
to command: HDBaseT extenders, HDMI splitters, audio extractors
(`AV-ROUTING.md` §3.4, task T3).

Each is a `tier: declarative` driver with no binary. It declares only what the
AV resolver needs to walk through it:

- **endpoints** — its ports, with `signal` and `connector`, so the binding page
  can wire cables to them;
- **fixed `switching`** — which input reaches which output, with no selector,
  because there is nothing to select;
- **`power_mode: ALWAYS_ON`** — the resolver never powers it;
- **`timing.settle_ms`** — how long it takes to pass a signal on, which the
  resolver adds when it works out when a display may safely select its input
  (an HDMI stage re-handshakes, and a TV that selects too early latches
  "no signal").

| Package | Ports | Settle |
|---|---|---|
| `com.notrix.passive.hdbaset-tx` | `hdmi_in` → `hdbt_out` | 300 ms |
| `com.notrix.passive.hdbaset-rx` | `hdbt_in` → `hdmi_out` | 300 ms |
| `com.notrix.passive.hdmi-splitter-1x2` | `hdmi_in` → `hdmi_out1`, `hdmi_out2` | 500 ms |
| `com.notrix.passive.hdmi-audio-extractor` | `hdmi_in` → `hdmi_out`, `optical_out`, `analog_out` | 500 ms |

The settle times are typical figures, not measurements of any particular box.
Copy a package and change `settle_ms` (and the ports) for a specific model.

The endpoints sit inside `cap.av_output@v1`, a capability with no state and no
commands: v2 manifests carry endpoints inside the capability that owns them, and
a passive box's only capability is passing media through.

`controller-core`'s `TestWrittenAVDriversValidate` installs-checks these files
when this repository sits beside it.
