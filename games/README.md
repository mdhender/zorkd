# Z-Machine Story Files

Z-machine story files the server serves.

`games.go` embeds them into the executable with `go:embed` and is the only place a story
file is named; the `.z3` files and their licenses are third-party works and are not covered
by this repository's license.

Save files are stored separately.

## Story files

Story files are served to users.
The server never writes or updates them.

Each game has its own directory holding the story file and the license that governs it:

```
games/
    zork1/
        zork1-r119-880429.z3
        LICENSE
    zork2/
        zork2-r63-860811.z3
        LICENSE
    zork3/
        zork3-r25-860811.z3
        LICENSE
```

Each story file name carries the release number and serial number from the story's own header:

```
zork1-r119-880429.z3
      ^^^^ ^^^^^^
      |    serial number
      release number
```

Saves only work with the exact story they were made from.
Putting those two numbers in the name makes a mismatched pair obvious at a glance, instead of turning into a puzzling checksum error later.

| File | Game | Version | Release | Serial | Checksum | Source |
|---|---|---|---|---|---|---|
| `zork1/zork1-r119-880429.z3` | Zork I | 3 | 119 | 880429 | `0xbf44` | [historicalsource/zork1](https://github.com/historicalsource/zork1), `COMPILED/zork1.z3` |
| `zork2/zork2-r63-860811.z3` | Zork II | 3 | 63 | 860811 | `0x4492` | [historicalsource/zork2](https://github.com/historicalsource/zork2), `COMPILED/zork2.z3` |
| `zork3/zork3-r25-860811.z3` | Zork III | 3 | 25 | 860811 | `0xf645` | [historicalsource/zork3](https://github.com/historicalsource/zork3), `COMPILED/zork3.z3` |

All three carry a checksum at offset `$1c`, so none needs the computed-checksum fallback that
Quetzal requires of stories predating that field.

Microsoft, Team Xbox, and Activision released the compiled Zork files under the MIT License.
Each game directory has its own `LICENSE`, and that file is the one that applies.
Read it before you reuse a story file for anything.
