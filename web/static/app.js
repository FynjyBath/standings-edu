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
  //                  [data-filter-count="#счётчик"] [data-filter-empty="#пусто"]>
  // У каждой строки текст для поиска берётся из data-filter-text, иначе из
  // textContent. Скрытие — классом .filtered-out (см. styles.css).
  function initTableFilters() {
    var inputs = document.querySelectorAll("[data-filter-rows]");
    [].forEach.call(inputs, function (input) {
      var rowsSel = input.getAttribute("data-filter-rows");
      var countNode = sel(input.getAttribute("data-filter-count"));
      var emptyNode = sel(input.getAttribute("data-filter-empty"));

      function rowText(r) {
        var t = r.getAttribute("data-filter-text");
        if (t == null) t = r.textContent || "";
        return t.toLowerCase();
      }
      function apply() {
        var q = norm(input.value);
        var rows = document.querySelectorAll(rowsSel);
        var total = rows.length, shown = 0;
        [].forEach.call(rows, function (r) {
          var match = !q || rowText(r).indexOf(q) !== -1;
          r.classList.toggle("filtered-out", !match);
          if (match) shown++;
        });
        if (countNode) countNode.textContent = q ? (shown + " из " + total) : String(total);
        if (emptyNode) emptyNode.hidden = shown !== 0 || total === 0;
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
      });
    }

    // Чипы — только навигация-якори (клик прокручивает к контесту). Активный
    // контест не подсвечиваем по просьбе: без scroll-spy и без is-current.
  }

  function sel(s) { return s ? document.querySelector(s) : null; }

  function init() {
    initTableFilters();
    initSearchableSelects();
    initContestTOC();
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
