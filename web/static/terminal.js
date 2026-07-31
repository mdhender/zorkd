// Keeping the terminal looking and behaving like a terminal.
//
// Everything here is presentation and convenience. With it disabled the page
// still works: the form posts, the server answers, and the whole transcript is
// there — the player just scrolls it themselves and has no command history.
//
// Nothing here is authoritative. The history is this browser's memory of what
// was typed, not the game's; the game is on the server.

(function () {
    "use strict";

    // MAX_HISTORY bounds what one game keeps. It is a recall list, not a log.
    var MAX_HISTORY = 100;

    // ---- storage ---------------------------------------------------------
    //
    // Storage can be absent or refused — private browsing, or a browser told
    // not to keep anything. Every use is guarded, and a failure means the
    // feature quietly does less rather than the page breaking.

    function read(key) {
        try {
            return localStorage.getItem(key);
        } catch (e) {
            return null;
        }
    }

    function write(key, value) {
        try {
            localStorage.setItem(key, value);
        } catch (e) {
            // Nothing to be done, and nothing worth telling the player.
        }
    }

    function historyKey(gameID) {
        return "zorkd.history." + gameID;
    }

    function loadHistory(gameID) {
        var stored = read(historyKey(gameID));
        if (!stored) {
            return [];
        }
        try {
            var parsed = JSON.parse(stored);
            return Array.isArray(parsed) ? parsed.filter(function (item) {
                return typeof item === "string";
            }) : [];
        } catch (e) {
            return [];
        }
    }

    function saveHistory(gameID, history) {
        write(historyKey(gameID), JSON.stringify(history));
    }

    // ---- the screen ------------------------------------------------------

    function transcript() {
        return document.getElementById("transcript");
    }

    function toBottom() {
        var box = transcript();
        if (box) {
            box.scrollTop = box.scrollHeight;
        }
    }

    function setNotice(message) {
        var notice = document.getElementById("notice");
        if (notice) {
            notice.textContent = message;
        }
    }

    // A refresh redraws the whole game from the server, so the newest text is
    // at the bottom of a box that opens at the top. Start where the player left
    // off rather than at the copyright notice.
    document.addEventListener("DOMContentLoaded", toBottom);
    document.addEventListener("htmx:afterSwap", toBottom);

    // Typing means typing at the prompt. A terminal has one place for input,
    // and hunting for it with the mouse is not it. Focus is only taken when
    // nothing else has it, so the transcript can still be read with the
    // keyboard and the settings buttons still answer to the space bar.
    document.addEventListener("keydown", function (event) {
        if (document.activeElement !== document.body) {
            return;
        }
        if (event.ctrlKey || event.metaKey || event.altKey || event.key.length !== 1) {
            return;
        }
        var command = document.getElementById("command");
        if (command) {
            command.focus();
        }
    });

    // ---- Alpine components -----------------------------------------------

    document.addEventListener("alpine:init", function () {
        // prompt is the command line: what is being typed, and what was typed
        // before it.
        //
        // The history is browsed the way a shell browses one. The line being
        // written is kept aside when browsing starts, so arrowing up to look at
        // something and back down again returns what was half-typed rather than
        // an empty field.
        Alpine.data("prompt", function (gameID) {
            return {
                command: "",
                history: [],
                cursor: 0,
                draft: "",

                init: function () {
                    this.history = loadHistory(gameID);
                    this.cursor = this.history.length;
                },

                starting: function () {
                    setNotice("");
                },

                finished: function (event) {
                    // A turn that failed keeps the line, so it can be sent
                    // again without being typed again.
                    if (!event.detail || !event.detail.successful) {
                        return;
                    }

                    this.remember(this.command.trim());
                    this.command = "";
                    this.cursor = this.history.length;

                    var input = this.$refs.command;
                    this.$nextTick(function () {
                        input.focus();
                    });
                },

                failed: function () {
                    setNotice("The server could not play that turn. Nothing was lost — try again.");
                },

                remember: function (line) {
                    if (!line || this.history[this.history.length - 1] === line) {
                        return;
                    }
                    this.history.push(line);
                    if (this.history.length > MAX_HISTORY) {
                        this.history.shift();
                    }
                    saveHistory(gameID, this.history);
                },

                earlier: function () {
                    if (this.cursor === this.history.length) {
                        this.draft = this.command;
                    }
                    if (this.cursor > 0) {
                        this.cursor -= 1;
                        this.command = this.history[this.cursor];
                    }
                },

                later: function () {
                    if (this.cursor >= this.history.length) {
                        return;
                    }
                    this.cursor += 1;
                    this.command = this.cursor === this.history.length
                        ? this.draft
                        : this.history[this.cursor];
                },
            };
        });

        // preferences are the screen's, and this browser's. They are written to
        // the root element, which is where the stylesheet reads them, and the
        // inline script in the page head applies them again before the first
        // paint so a chosen amber screen never flashes green.
        Alpine.data("preferences", function () {
            return {
                phosphor: document.documentElement.dataset.phosphor || "green",
                crt: document.documentElement.dataset.crt === "on",

                choose: function (colour) {
                    this.phosphor = colour;
                    document.documentElement.dataset.phosphor = colour;
                    write("zorkd.phosphor", colour);
                },

                toggleCRT: function () {
                    this.crt = !this.crt;
                    if (this.crt) {
                        document.documentElement.dataset.crt = "on";
                    } else {
                        delete document.documentElement.dataset.crt;
                    }
                    write("zorkd.crt", this.crt ? "on" : "off");
                },
            };
        });
    });
})();
