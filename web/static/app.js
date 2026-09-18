// Общие клиентские улучшения списков и таблиц: живые фильтры строк, поисковые
// выпадашки (вместо огромных <select>) и оглавление контестов. Все инициализаторы
// безопасны на страницах, где нужных элементов нет (просто ничего не делают).
(function () {
  "use strict";

  function norm(s) {
    return (s == null ? "" : String(s)).toLowerCase().trim();
  }

  // ── 1. Живой фильтр строк таблицы/списка ────────────────────────────────
  // Разметка: <input data-filter-rows="СЕЛЕКТОР_СТРОК"
  //                  [data-filter-count="#счётчик"] [data-filter-empty="#пусто"]
  //                  [data-filter-fold="#details"]>
  // У каждой строки текст для поиска берётся из data-filter-text, иначе из
  // textContent. Скрытие — классом .filtered-out (см. styles.css).
  // data-filter-fold — свёрнутый блок (обычно архив), строки которого тоже
  // участвуют в поиске: при совпадениях внутри он раскрывается сам, а его
  // счётчик (.archive-fold-count) считает найденное отдельно, чтобы основной
  // счётчик не мешал архив с активными.
  function initTableFilters() {
    var inputs = document.querySelectorAll("[data-filter-rows]");
    [].forEach.call(inputs, function (input) {
      var rowsSel = input.getAttribute("data-filter-rows");
      var countNode = sel(input.getAttribute("data-filter-count"));
      var emptyNode = sel(input.getAttribute("data-filter-empty"));
      var foldNode = sel(input.getAttribute("data-filter-fold"));
      var foldCount = foldNode ? foldNode.querySelector(".archive-fold-count") : null;

      function rowText(r) {
        var t = r.getAttribute("data-filter-text");
        if (t == null) t = r.textContent || "";
        return t.toLowerCase();
      }
      function inFold(r) { return !!foldNode && foldNode.contains(r); }
      function apply() {
        var q = norm(input.value);
        var rows = document.querySelectorAll(rowsSel);
        var total = 0, shown = 0, foldTotal = 0, foldShown = 0;
        [].forEach.call(rows, function (r) {
          var match = !q || rowText(r).indexOf(q) !== -1;
          r.classList.toggle("filtered-out", !match);
          if (inFold(r)) {
            foldTotal++;
            if (match) foldShown++;
            return;
          }
          total++;
          if (match) shown++;
        });
        if (foldCount) foldCount.textContent = q ? (foldShown + " из " + foldTotal) : String(foldTotal);
        // Нашлось только в архиве — раскрываем, иначе совпадения не видно.
        // Обратно не закрываем: раскрытый вручную блок захлопывать невежливо.
        if (foldNode && q && foldShown > 0) foldNode.open = true;
        // Свёрнутая таблица (data-collapse-rows) на время поиска раскрывается,
        // иначе совпадения ниже порога остались бы скрыты; очистка запроса
        // возвращает свёртку.
        if (rows.length) {
          var table = rows[0].closest ? rows[0].closest("table") : null;
          if (table) table.classList.toggle("filter-active", !!q);
        }
        if (countNode) countNode.textContent = q ? (shown + " из " + total) : String(total);
        // Плашка «ничего не найдено» — только при активном поиске без совпадений
        // (в том числе в архиве); без запроса её не показываем никогда.
        if (emptyNode) emptyNode.hidden = !q || shown + foldShown !== 0 || total + foldTotal === 0;
      }
      input.addEventListener("input", apply);
      apply();
    });
  }

  // ── 2. Поисковая выпадашка вместо большого <select> ─────────────────────
  // <select data-searchable [data-placeholder="…"]> прогрессивно заменяется
  // комбобоксом: текстовое поле фильтрует список опций, выбор пишет value
  // обратно в скрытый <select> и шлёт 'change'. Без JS работает обычный select.
  function initSearchableSelects() {
    var selects = document.querySelectorAll("select[data-searchable]");
    [].forEach.call(selects, function (select) {
      if (select.dataset.comboReady) return;
      select.dataset.comboReady = "1";

      var options = [].filter.call(select.options, function (o) { return o.value !== ""; })
        .map(function (o) { return { value: o.value, label: o.textContent.trim() }; });
      var placeholder = select.getAttribute("data-placeholder") ||
        (select.options.length && select.options[0].value === "" ? select.options[0].textContent.trim() : "Поиск…");

      var wrap = document.createElement("div");
      wrap.className = "combo";
      var input = document.createElement("input");
      input.type = "text";
      input.className = "combo-input";
      input.setAttribute("role", "combobox");
      input.setAttribute("autocomplete", "off");
      input.setAttribute("aria-expanded", "false");
      input.placeholder = placeholder;
      var list = document.createElement("ul");
      list.className = "combo-list";
      list.hidden = true;
      wrap.appendChild(input);
      wrap.appendChild(list);
      select.style.display = "none";
      select.parentNode.insertBefore(wrap, select.nextSibling);

      var active = -1, filtered = options.slice();

      function render() {
        var q = norm(input.value);
        filtered = q ? options.filter(function (o) { return o.label.toLowerCase().indexOf(q) !== -1; }) : options.slice();
        list.innerHTML = "";
        if (!filtered.length) {
          var li = document.createElement("li");
          li.className = "combo-empty";
          li.textContent = "ничего не найдено";
          list.appendChild(li);
        } else {
          filtered.forEach(function (o, i) {
            var li = document.createElement("li");
            li.className = "combo-item" + (i === active ? " is-active" : "");
            li.textContent = o.label;
            li.setAttribute("data-value", o.value);
            li.addEventListener("mousedown", function (e) { e.preventDefault(); choose(o); });
            list.appendChild(li);
          });
        }
      }
      function open() { list.hidden = false; input.setAttribute("aria-expanded", "true"); render(); }
      function close() { list.hidden = true; input.setAttribute("aria-expanded", "false"); active = -1; }
      function choose(o) {
        select.value = o.value;
        input.value = o.label;
        select.dispatchEvent(new Event("change", { bubbles: true }));
        close();
      }
      function move(delta) {
        if (list.hidden) { open(); return; }
        if (!filtered.length) return;
        active = (active + delta + filtered.length) % filtered.length;
        render();
        var el = list.querySelector(".is-active");
        if (el) el.scrollIntoView({ block: "nearest" });
      }

      input.addEventListener("focus", open);
      input.addEventListener("input", function () {
        select.value = ""; // ввод сбрасывает выбор до подтверждения
        active = -1; open();
      });
      input.addEventListener("keydown", function (e) {
        if (e.key === "ArrowDown") { e.preventDefault(); move(1); }
        else if (e.key === "ArrowUp") { e.preventDefault(); move(-1); }
        else if (e.key === "Enter") {
          if (!list.hidden && active >= 0 && filtered[active]) { e.preventDefault(); choose(filtered[active]); }
        } else if (e.key === "Escape") { close(); }
      });
      document.addEventListener("click", function (e) {
        if (!wrap.contains(e.target)) close();
      });
    });
  }

  // ── 3. Оглавление контестов ─────────────────────────────────────────────
  // <nav data-contest-toc> заполняется чипами-якорями по .contest-block[id^=contest-].
  // Есть поле фильтра (прячет и чипы, и сами блоки) и подсветка активного чипа.
  function initContestTOC() {
    var nav = document.querySelector("[data-contest-toc]");
    if (!nav) return;
    var blocks = [].slice.call(document.querySelectorAll(".contest-block[id^='contest-']"));
    if (blocks.length < 2) { nav.hidden = true; return; }
    nav.hidden = false;

    var filter = nav.querySelector("[data-toc-filter]");
    var chipsBox = nav.querySelector("[data-toc-chips]") || nav;
    var empty = nav.querySelector("[data-toc-empty]");
    var byId = {};

    blocks.forEach(function (b) {
      var h = b.querySelector("h2");
      var title = h ? h.textContent.trim() : b.id;
      var chip = document.createElement("a");
      chip.className = "toc-chip";
      chip.href = "#" + b.id;
      chip.textContent = title;
      chip.title = title; // полное название — в подсказке (чип обрезается многоточием)
      chip.setAttribute("data-toc-target", b.id);
      chipsBox.appendChild(chip);
      byId[b.id] = { block: b, chip: chip, text: title.toLowerCase() };
    });

    var totalNode = nav.querySelector("[data-toc-total]");
    if (totalNode) totalNode.textContent = blocks.length;

    // ── Сворачивание оглавления ───────────────────────────────────────────
    // У больших групп чипов несколько десятков, и оглавление занимает пол-экрана.
    // Показываем два ряда, остальное — по кнопке; выбор запоминаем.
    var toggle = nav.querySelector("[data-toc-toggle]");
    var storageKey = "nemalo-toc-expanded";
    var expanded = false;
    try { expanded = localStorage.getItem(storageKey) === "1"; } catch (e) {}
    var forcedOpen = false; // раскрыто фильтром, не пользователем

    function hiddenChipCount() {
      // Чип считается скрытым, если не влезает в свёрнутую высоту.
      var limit = chipsBox.clientHeight;
      var n = 0;
      Object.keys(byId).forEach(function (id) {
        var chip = byId[id].chip;
        if (chip.classList.contains("filtered-out") || chip.classList.contains("student-hidden")) return;
        if (chip.offsetTop + chip.offsetHeight > limit + 1) n++;
      });
      return n;
    }

    function syncToggle() {
      if (!toggle) return;
      var open = expanded || forcedOpen;
      nav.classList.toggle("contest-toc--collapsed", !open);
      // Помещается целиком — кнопка не нужна.
      var overflowing = chipsBox.scrollHeight > chipsBox.clientHeight + 1;
      if (open) {
        toggle.hidden = false;
        toggle.textContent = "Свернуть";
      } else if (overflowing) {
        toggle.hidden = false;
        var n = hiddenChipCount();
        toggle.textContent = n > 0 ? "Ещё " + n : "Показать все";
      } else {
        toggle.hidden = true;
      }
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
    }

    if (toggle) {
      toggle.addEventListener("click", function () {
        expanded = !(expanded || forcedOpen);
        forcedOpen = false;
        try { localStorage.setItem(storageKey, expanded ? "1" : "0"); } catch (e) {}
        syncToggle();
      });
    }
    // Число скрытых чипов меняется при переносе строк и при работе фильтров.
    if (window.ResizeObserver) {
      new ResizeObserver(function () { syncToggle(); }).observe(chipsBox);
    }
    nav.classList.add("contest-toc--collapsed");
    syncToggle();

    if (filter) {
      filter.addEventListener("input", function () {
        var q = norm(filter.value);
        var shown = 0;
        Object.keys(byId).forEach(function (id) {
          var it = byId[id];
          var match = !q || it.text.indexOf(q) !== -1;
          it.chip.classList.toggle("filtered-out", !match);
          it.block.classList.toggle("filtered-out", !match);
          if (match) shown++;
        });
        if (empty) empty.hidden = shown !== 0;
        // На время поиска раскрываем: иначе найденное осталось бы в скрытых
        // рядах. Пустой запрос возвращает прежнее состояние.
        forcedOpen = !!q;
        syncToggle();
      });
    }

    // Клик по чипу: нативный якорь ненадёжен — если целевой блок ещё не
    // подгружен (ленивая загрузка) или таблица свернётся после отрисовки,
    // высота страницы меняется уже после прыжка и попадание «мажет». Поэтому
    // прокручиваем сами: сначала просим блок загрузиться, ждём появления
    // таблицы, и только потом scrollIntoView (плюс повтор на следующем кадре).
    chipsBox.addEventListener("click", function (e) {
      var chip = e.target.closest ? e.target.closest(".toc-chip") : null;
      if (!chip) return;
      var id = chip.getAttribute("data-toc-target");
      var target = id && document.getElementById(id);
      if (!target) return;
      e.preventDefault();
      history.replaceState(null, "", "#" + id);

      function settle() {
        target.scrollIntoView({ block: "start" });
        // Второй заход после перерисовки: свёртка и вставленные строки могли
        // сдвинуть блок, пока браузер применял первый скролл.
        requestAnimationFrame(function () { target.scrollIntoView({ block: "start" }); });
      }
      if (!target.classList.contains("contest-lazy")) { settle(); return; }
      // Ленивый блок: жмём его кнопку загрузки и ждём появления таблицы.
      var btn = target.querySelector(".lazy-load-btn");
      if (btn) btn.click();
      var waited = 0;
      var timer = setInterval(function () {
        waited += 60;
        if (target.querySelector("table") || waited > 6000) {
          clearInterval(timer);
          settle();
        }
      }, 60);
    });
  }

  // ── 4. Свёртка длинных таблиц ────────────────────────────────────────────
  // <table data-collapse-rows="30">: если строк больше порога — показываются
  // первые 30 и кнопка «Показать всех (N)». Работает и для таблиц с двумя tbody
  // (обычный вид/без дорешки): порог применяется к каждому tbody через CSS.
  function initRowCollapse(scope) {
    var tables = (scope || document).querySelectorAll("table[data-collapse-rows]");
    [].forEach.call(tables, function (table) {
      if (table.dataset.collapseReady) return;
      table.dataset.collapseReady = "1";
      var limit = parseInt(table.getAttribute("data-collapse-rows"), 10) || 30;
      var maxRows = 0;
      [].forEach.call(table.tBodies, function (tb) {
        if (tb.rows.length > maxRows) maxRows = tb.rows.length;
      });
      if (maxRows <= limit) return;

      table.classList.add("rows-collapsed");
      var wrap = table.closest(".table-wrap") || table;
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "rows-expand-btn";
      btn.textContent = "Показать всех (" + maxRows + ")";
      btn.addEventListener("click", function () {
        table.classList.remove("rows-collapsed");
        btn.remove();
      });
      wrap.insertAdjacentElement("afterend", btn);
    });
  }

  // ── 5. Фильтры прогонов ejudge ───────────────────────────────────────────
  // В судейский интерфейс ejudge нельзя сослаться на ученика или задачу: вход
  // адресуется контестом, внутри — прогоны всех участников. Зато там есть поле
  // фильтра, и нужную строку сервер уже сложил в data-ejudge-filter. При клике
  // по ссылке кладём её в буфер обмена — остаётся вставить в поле фильтра.
  //
  // Обработчик делегированный: ссылки появляются и в лениво подгруженных
  // таблицах, и в сводной, которая рисуется на клиенте.
  function initEjudgeFilters() {
    if (window.__ejudgeFilterReady) return;
    window.__ejudgeFilterReady = true;

    var hint = null, hintTimer = null;
    function flash(text, ok) {
      if (!hint) {
        hint = document.createElement("div");
        hint.className = "ejudge-filter-toast";
        document.body.appendChild(hint);
      }
      hint.textContent = text;
      hint.classList.toggle("ejudge-filter-toast--error", !ok);
      hint.classList.add("is-visible");
      if (hintTimer) clearTimeout(hintTimer);
      hintTimer = setTimeout(function () { hint.classList.remove("is-visible"); }, 2600);
    }
    function copy(text) {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        return navigator.clipboard.writeText(text);
      }
      return new Promise(function (resolve, reject) {
        try {
          var ta = document.createElement("textarea");
          ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
          document.body.appendChild(ta); ta.select();
          document.execCommand("copy"); document.body.removeChild(ta);
          resolve();
        } catch (e) { reject(e); }
      });
    }

    document.addEventListener("click", function (e) {
      var link = e.target.closest ? e.target.closest("[data-ejudge-filter]") : null;
      if (!link) return;
      var filter = link.getAttribute("data-ejudge-filter") || "";
      if (!filter) return;
      // Ссылку не перехватываем: она открывается как обычно (в новой вкладке),
      // копирование идёт параллельно.
      copy(filter).then(
        function () { flash("Фильтр скопирован: " + filter, true); },
        function () { flash("Не удалось скопировать. Фильтр: " + filter, false); }
      );
    });
  }

  // ── 6. Фильтр по ученику на странице группы ──────────────────────────────
  // Оставляет в каждой таблице только строки выбранного ученика и прячет
  // контесты, где его нет, — чтобы «посмотреть одного» не значило листать всё.
  //
  // Особенность страницы: часть таблиц подгружается лениво. Пока блок не
  // открыт, в нём не видно ни строк ученика, ни того, что его там нет, поэтому
  // при активном фильтре остаток догружается (хук standingsLoadAllLazy из
  // разметки страницы). Блоки прячем своим классом, а не общим .filtered-out:
  // тот занят фильтром оглавления, и два фильтра не должны спорить.
  function initStudentFilter() {
    var bar = document.querySelector("[data-student-filter]");
    if (!bar) return;
    var input = bar.querySelector("[data-student-filter-input]");
    var reset = bar.querySelector("[data-student-filter-reset]");
    var countNode = bar.querySelector("[data-student-filter-count]");
    if (!input) return;

    var loadedAll = false;

    function blocks() {
      return [].slice.call(document.querySelectorAll(".contest-block"));
    }
    // Чип оглавления, ведущий на этот блок (у оглавления свой фильтр).
    function chipFor(block) {
      if (!block.id) return null;
      return document.querySelector('[data-toc-target="' + block.id + '"]');
    }

    function apply() {
      var q = norm(input.value);
      var active = !!q;
      if (reset) reset.hidden = !active;

      // Считаем только контесты: доска почёта строки тоже фильтрует, но в
      // «в скольких таблицах есть ученик» ей не место — там всегда все.
      var withStudent = 0, totalContests = 0;
      blocks().forEach(function (block) {
        var rows = block.querySelectorAll("tr[data-filter-text]");
        if (!rows.length) return; // блок без строк (ленивая заглушка) не трогаем
        var isContest = block.id && block.id.indexOf("contest-") === 0;
        if (isContest) totalContests++;
        var shown = 0;
        [].forEach.call(rows, function (tr) {
          var match = !active || (tr.getAttribute("data-filter-text") || "").toLowerCase().indexOf(q) !== -1;
          tr.classList.toggle("filtered-out", !match);
          if (match) shown++;
        });
        // Свёртка длинных таблиц на время поиска раскрывается, иначе строка
        // ученика ниже порога осталась бы скрытой.
        [].forEach.call(block.querySelectorAll("table"), function (table) {
          table.classList.toggle("filter-active", active);
        });
        var hide = active && shown === 0;
        block.classList.toggle("student-hidden", hide);
        var chip = chipFor(block);
        if (chip) chip.classList.toggle("student-hidden", hide);
        if (isContest && !hide) withStudent++;
      });

      if (countNode) {
        if (!active) {
          countNode.textContent = "";
        } else if (withStudent === 0) {
          countNode.textContent = "ничего не найдено";
        } else {
          countNode.textContent = "в " + withStudent + " из " + totalContests + " " +
            plural(totalContests, "контеста", "контестов", "контестов");
        }
      }
    }

    function plural(n, one, few, many) {
      var d10 = n % 10, d100 = n % 100;
      if (d10 === 1 && d100 !== 11) return one;
      if (d10 >= 2 && d10 <= 4 && (d100 < 10 || d100 >= 20)) return few;
      return many;
    }

    function onInput() {
      var active = !!norm(input.value);
      // Первый же ввод догружает отложенные таблицы: без них счёт «в скольких
      // таблицах есть ученик» был бы неверным.
      if (active && !loadedAll && typeof window.standingsLoadAllLazy === "function") {
        loadedAll = true;
        if (countNode) countNode.textContent = "загружаю таблицы…";
        window.standingsLoadAllLazy(function (left) {
          if (countNode && left > 0) countNode.textContent = "загружаю таблицы… осталось " + left;
        }).then(function () { apply(); });
      }
      apply();
    }

    input.addEventListener("input", onInput);
    if (reset) reset.addEventListener("click", function () {
      input.value = "";
      apply();
      input.focus();
    });
    apply();

    // Строки появляются и после ленивой подгрузки — фильтр применяем заново.
    window.standingsApplyStudentFilter = apply;
  }

  // ── 7. Подсказка «таблица продолжается вправо» ───────────────────────────
  // Широкие таблицы прокручиваются вбок внутри .table-wrap. Полосу прокрутки
  // рисует браузер (стили — в styles.css, чтобы она была видна всегда, а не
  // всплывала). Здесь только тень у правого края: без неё край таблицы легко
  // принять за её конец.
  //
  // Своей полосы здесь быть не должно: у контейнера уже есть родная, и вторая
  // рядом с ней — просто лишняя.
  function initWideTables() {
    var wraps = document.querySelectorAll(".table-wrap");
    [].forEach.call(wraps, function (wrap) {
      if (wrap.__wideReady) return;
      wrap.__wideReady = true;

      function edge() {
        wrap.classList.toggle(
          "has-more-right",
          wrap.scrollWidth - wrap.clientWidth - wrap.scrollLeft > 1
        );
      }
      // Пересчёт читает scrollWidth, то есть заставляет браузер считать
      // вёрстку. Сводная перестраивает таблицу на сотни строк — сводим к
      // одному разу на кадр.
      var pending = false;
      function schedule() {
        if (pending) return;
        pending = true;
        (window.requestAnimationFrame || function (f) { setTimeout(f, 16); })(function () {
          pending = false;
          edge();
        });
      }
      wrap.addEventListener("scroll", schedule);
      window.addEventListener("resize", schedule);
      if (window.ResizeObserver) {
        new ResizeObserver(schedule).observe(wrap);
      }
      if (window.MutationObserver) {
        new MutationObserver(schedule).observe(wrap, { childList: true, subtree: true });
      }
      edge();
    });
  }

  // ── 7. Запоминание свёрнутых блоков ─────────────────────────────────────
  // <details data-remember="ключ"> сохраняет своё состояние между страницами:
  // панель группы нужна изредка, но тому, кто ей пользуется, разворачивать её
  // каждый раз заново — лишняя работа.
  function initRememberedDetails() {
    var blocks = document.querySelectorAll("details[data-remember]");
    [].forEach.call(blocks, function (node) {
      var key = "nemalo-open-" + node.getAttribute("data-remember");
      try {
        var saved = localStorage.getItem(key);
        if (saved === "1") node.open = true;
        else if (saved === "0") node.open = false;
      } catch (e) {}
      node.addEventListener("toggle", function () {
        try { localStorage.setItem(key, node.open ? "1" : "0"); } catch (e) {}
      });
    });
  }

  function sel(s) { return s ? document.querySelector(s) : null; }

  function init() {
    initTableFilters();
    initSearchableSelects();
    initContestTOC();
    initRowCollapse(document);
    initEjudgeFilters();
    initStudentFilter();
    initRememberedDetails();
    initWideTables();
  }
  // Для динамически вставленных фрагментов (ленивые таблицы контестов).
  window.standingsInitScope = function (scope) {
    initRowCollapse(scope);
    // У подгруженного блока своя таблица — ей тоже нужна полоса прокрутки.
    initWideTables();
    // В подгруженном блоке появились строки — применяем к ним активный фильтр.
    if (typeof window.standingsApplyStudentFilter === "function") {
      window.standingsApplyStudentFilter();
    }
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
