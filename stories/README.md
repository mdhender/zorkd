# Z-Machine Story Files

Z-machine story files the server serves.
Nothing here is part of the package.

Save files are stored separately.

## Story files

Story files are served to users.
The package never writes or updates them.

Each name carries the release number and serial number from the story's own header:

```
zork1-r119-880429.z3
      ^^^^ ^^^^^^
      |    serial number
      release number
```

Saves only work with the exact story they were made from.
Putting those two numbers in the name makes a mismatched pair obvious at a glance, instead of turning into a puzzling checksum error later.

| File | Game | Version | Release | Serial | Source |
|---|---|---|---|---|---|
| `zork1-r119-880429.z3` | Zork I | 3 | 119 | 880429 | [historicalsource/zork1](https://github.com/historicalsource/zork1), `COMPILED/zork1.z3` |
| `zork2-r63-860811.z3` | Zork II | 3 | 63 | 860811 | [historicalsource/zork2](https://github.com/historicalsource/zork2), `COMPILED/zork2.z3` |
| `zork3-r25-860811.z3` | Zork III | 3 | 25 | 860811 | [historicalsource/zork3](https://github.com/historicalsource/zork3), `COMPILED/zork3.z3` |

Microsoft, Team Xbox, and Activision released the compiled Zork files under the MIT License.
Each story file has its own license file next to it, and that file is the one that applies.
Read it before you reuse a story file for anything.
