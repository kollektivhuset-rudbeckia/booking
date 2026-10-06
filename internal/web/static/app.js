// Small progressive enhancements. The site works without any of this.
(function () {
	'use strict';

	// --- Theme toggle -------------------------------------------------------
	var root = document.documentElement;
	var stored = null;
	try { stored = localStorage.getItem('rb-theme'); } catch (e) { /* private mode */ }
	if (stored === 'light' || stored === 'dark') {
		root.setAttribute('data-theme', stored);
	}
	var toggle = document.querySelector('[data-theme-toggle]');
	if (toggle) {
		toggle.addEventListener('click', function () {
			var dark = root.getAttribute('data-theme') === 'dark' ||
				(root.getAttribute('data-theme') !== 'light' &&
					window.matchMedia('(prefers-color-scheme: dark)').matches);
			var next = dark ? 'light' : 'dark';
			root.setAttribute('data-theme', next);
			try { localStorage.setItem('rb-theme', next); } catch (e) { /* ignore */ }
		});
	}

	// --- Confirm before destructive submits ---------------------------------
	document.querySelectorAll('form[data-confirm]').forEach(function (form) {
		form.addEventListener('submit', function (event) {
			if (!window.confirm(form.getAttribute('data-confirm'))) {
				event.preventDefault();
			}
		});
	});

	// --- Keep the chosen day in view ----------------------------------------
	var selected = document.querySelector('.daytab.is-selected');
	if (selected && selected.scrollIntoView) {
		selected.scrollIntoView({ block: 'nearest', inline: 'center' });
	}

	// --- Live slot list while typing an own length ---------------------------
	// The page works without this: the form submits normally and the server
	// renders the same result. This only saves the trip.
	var lengthForm = document.querySelector('form[data-live-duration]');
	var slotArea = document.getElementById('slot-area');
	var feedback = document.getElementById('custom-feedback');

	if (lengthForm && slotArea && feedback && window.fetch && window.AbortController) {
		var input = lengthForm.querySelector('input[name="langd"]');
		var submit = lengthForm.querySelector('button[type="submit"]');
		var pending = null;
		var timer = null;
		var latest = 0;

		// Pressing Enter still submits, so the button is redundant now.
		if (submit) { submit.hidden = true; }

		var apply = function (html, url) {
			var doc = new DOMParser().parseFromString(html, 'text/html');
			var nextFeedback = doc.getElementById('custom-feedback');
			var nextSlots = doc.getElementById('slot-area');
			if (!nextFeedback || !nextSlots) { return; }

			feedback.innerHTML = nextFeedback.innerHTML;

			// A rejected length leaves the times alone rather than snapping
			// them back to a default, which would jump around mid-typing.
			if (!nextFeedback.querySelector('.alert')) {
				slotArea.innerHTML = nextSlots.innerHTML;
				var track = document.getElementById('timeline-track');
				var nextTrack = doc.getElementById('timeline-track');
				if (track && nextTrack) { track.innerHTML = nextTrack.innerHTML; }
				history.replaceState(null, '', url);
			}
		};

		var update = function () {
			if (!input.value.trim()) { return; }

			var url = lengthForm.getAttribute('action') + '?' +
				new URLSearchParams(new FormData(lengthForm)).toString();

			if (pending) { pending.abort(); }
			pending = new AbortController();

			var mine = ++latest;
			slotArea.setAttribute('aria-busy', 'true');
			slotArea.classList.add('is-loading');

			fetch(url, { signal: pending.signal, headers: { 'Accept': 'text/html' } })
				.then(function (response) {
					if (!response.ok) { throw new Error('status ' + response.status); }
					return response.text();
				})
				.then(function (html) {
					// A slower earlier request must not overwrite a newer one.
					if (mine !== latest) { return; }
					apply(html, url);
				})
				.catch(function (err) {
					if (err.name === 'AbortError') { return; }
					// Give the button back so the member is never stuck.
					if (submit) { submit.hidden = false; }
				})
				.finally(function () {
					if (mine !== latest) { return; }
					slotArea.removeAttribute('aria-busy');
					slotArea.classList.remove('is-loading');
				});
		};

		input.addEventListener('input', function () {
			clearTimeout(timer);
			timer = setTimeout(update, 350);
		});
		// Enter should act immediately rather than wait out the debounce.
		lengthForm.addEventListener('submit', function (event) {
			event.preventDefault();
			clearTimeout(timer);
			update();
		});
	}

	// --- Mattermost user picker ---------------------------------------------
	// The field is a plain text input that submits a username. This turns it
	// into a combobox: the whole list of people who may book is fetched once,
	// indexed by members.js, and searched as you type — by full name or by
	// username, whichever you happen to know. Without JavaScript, or without a
	// chat server, typing the username by hand does exactly the same thing.
	document.querySelectorAll('input[data-member-search]').forEach(function (field) {
		var list = document.getElementById(field.getAttribute('aria-controls'));
		if (!list || !window.fetch || !window.RBMembers) { return; }

		var nameSelector = field.getAttribute('data-member-name');
		var nameField = nameSelector ? document.querySelector(nameSelector) : null;
		var index = null;      // the searchable directory, once fetched
		var remote = false;    // too many members to hold: let the server search
		var loading = null;    // the fetch in flight, so it happens once
		var shown = [];        // what the list currently offers
		var active = -1;       // which option the keyboard is on
		var wait = null;
		var inflight = null;

		var close = function () {
			list.hidden = true;
			list.innerHTML = '';
			field.setAttribute('aria-expanded', 'false');
			field.removeAttribute('aria-activedescendant');
			shown = [];
			active = -1;
		};

		var highlight = function (next) {
			var options = list.children;
			if (!options.length) { return; }
			if (active >= 0 && options[active]) {
				options[active].removeAttribute('aria-selected');
			}
			active = (next + options.length) % options.length;
			options[active].setAttribute('aria-selected', 'true');
			field.setAttribute('aria-activedescendant', options[active].id);
			if (options[active].scrollIntoView) {
				options[active].scrollIntoView({ block: 'nearest' });
			}
		};

		var choose = function (user) {
			if (!user) { return; }
			// The form submits the username, so that is what the field holds.
			field.value = user.username;
			if (nameField && !nameField.value.trim()) {
				nameField.value = user.name || '';
			}
			close();
		};

		var render = function (users) {
			shown = users;
			list.innerHTML = '';
			if (!users.length) {
				close();
				return;
			}
			users.forEach(function (user, i) {
				var option = document.createElement('li');
				option.id = list.id + '-' + i;
				option.className = 'combo-option';
				option.setAttribute('role', 'option');
				var name = document.createElement('strong');
				name.textContent = user.name || user.username;
				var handle = document.createElement('span');
				handle.textContent = '@' + user.username;
				option.appendChild(name);
				option.appendChild(handle);
				// mousedown, not click: the field blurs before a click lands.
				option.addEventListener('mousedown', function (event) {
					event.preventDefault();
					choose(user);
				});
				list.appendChild(option);
			});
			list.hidden = false;
			field.setAttribute('aria-expanded', 'true');
			active = -1;
		};

		// ask lets the server search, for a house too large to send at once.
		var ask = function (term) {
			if (inflight) { inflight.abort(); }
			inflight = window.AbortController ? new AbortController() : null;
			fetch('/medlemmar?q=' + encodeURIComponent(term),
				{ signal: inflight ? inflight.signal : undefined, headers: { 'Accept': 'application/json' } })
				.then(function (response) {
					if (!response.ok) { throw new Error('status ' + response.status); }
					return response.json();
				})
				.then(function (data) { render(data.users || []); })
				.catch(function () { /* the field still works as plain text */ });
		};

		// load fetches the directory once, and remembers if it was too big.
		var load = function () {
			if (loading) { return loading; }
			loading = fetch('/medlemmar', { headers: { 'Accept': 'application/json' } })
				.then(function (response) {
					if (!response.ok) { throw new Error('status ' + response.status); }
					return response.json();
				})
				.then(function (data) {
					remote = !!data.truncated;
					index = window.RBMembers.buildIndex(data.users || []);
				})
				.catch(function () { index = null; });
			return loading;
		};

		var update = function () {
			var term = field.value.trim();
			if (term.length < 1) { close(); return; }
			if (remote) {
				if (term.length >= 2) { ask(term); }
				return;
			}
			load().then(function () {
				if (!index || field.value.trim() !== term) { return; }
				render(window.RBMembers.search(index, term));
			});
		};

		field.setAttribute('role', 'combobox');
		field.setAttribute('aria-expanded', 'false');
		field.setAttribute('aria-autocomplete', 'list');
		field.addEventListener('focus', load);
		field.addEventListener('input', function () {
			clearTimeout(wait);
			// The list is local, so there is nothing to wait for. The debounce
			// is only there for the server-side fallback.
			wait = setTimeout(update, remote ? 250 : 0);
		});

		field.addEventListener('keydown', function (event) {
			if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
				if (list.hidden) { update(); return; }
				event.preventDefault();
				highlight(active + (event.key === 'ArrowDown' ? 1 : -1));
				return;
			}
			if (event.key === 'Enter' && !list.hidden && active >= 0) {
				event.preventDefault();
				choose(shown[active]);
				return;
			}
			if (event.key === 'Escape' && !list.hidden) {
				event.preventDefault();
				close();
			}
		});

		field.addEventListener('blur', close);
	});

	// --- Guest rooms: keep check-out after check-in --------------------------
	var from = document.getElementById('fran');
	var to = document.getElementById('till');
	if (from && to) {
		var sync = function () {
			if (!from.value) { return; }
			var min = new Date(from.value);
			min.setDate(min.getDate() + 1);
			to.min = min.toISOString().slice(0, 10);
			if (to.value && to.value <= from.value) { to.value = to.min; }
		};
		from.addEventListener('change', sync);
		sync();
	}
})();
