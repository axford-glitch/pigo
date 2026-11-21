'use strict';
'require view';
'require fs';
'require ui';

function pigo(args) {
	return fs.exec('/usr/bin/pigo', ['feishu-ctl'].concat(args)).then(function(res) {
		var text = (res.stdout || '') + (res.stderr || '');
		if (res.code !== 0)
			throw new Error(text.trim() || 'pigo feishu-ctl failed');
		return res.stdout || '';
	});
}

function readStatus() {
	return pigo(['status']).then(function(stdout) {
		return JSON.parse(stdout);
	});
}

return view.extend({
	render: function() {
		var statusNode = E('pre', { 'id': 'pigo-status', 'style': 'white-space:pre-wrap' }, _('读取中…'));
		var qr = E('img', {
			'id': 'pigo-qr',
			'alt': 'Feishu QR',
			'style': 'display:none;width:280px;height:280px;background:#fff;padding:8px'
		});
		var code = E('div', { 'id': 'pigo-code' });
		var base = E('input', { 'id': 'pigo-base', 'class': 'cbi-input-text', 'style': 'width:100%' });
		var model = E('input', { 'id': 'pigo-model', 'class': 'cbi-input-text', 'style': 'width:100%' });
		var key = E('input', { 'id': 'pigo-key', 'class': 'cbi-input-text', 'type': 'password', 'style': 'width:100%', 'placeholder': _('留空则不修改') });
		var hint = E('div', { 'id': 'pigo-key-hint', 'class': 'cbi-value-description' });

		function showStatus(st) {
			statusNode.textContent = [
				st.bound ? _('已绑定') : _('未绑定'),
				st.open_id ? ('open_id: ' + st.open_id) : '',
				_('模型: ') + (st.model || ''),
				'API base: ' + (st.base_url || ''),
				_('协议: ') + (st.protocol || _('自动')),
				'API key: ' + (st.api_key_set ? st.api_key_hint : _('未设置')),
				_('绑定状态: ') + (st.bind_state || ''),
				st.message || ''
			].filter(Boolean).join('\n');
			if (!base.value)
				base.value = st.base_url || '';
			if (!model.value)
				model.value = st.model || '';
			hint.textContent = st.api_key_set ? (_('当前 key ') + st.api_key_hint) : '';
			if (st.user_code)
				code.textContent = _('校验码: ') + st.user_code;
		}

		function refreshQR() {
			return pigo(['qr']).then(function(b64) {
				qr.style.display = '';
				qr.src = 'data:image/png;base64,' + b64.trim();
			}).catch(function() {
				qr.style.display = 'none';
			});
		}

		var timer = null;
		function poll() {
			return pigo(['bind-poll']).then(function(stdout) {
				var st = JSON.parse(stdout);
				showStatus(st);
				if (st.bind_state === 'pending')
					return refreshQR();
				if (timer) {
					window.clearInterval(timer);
					timer = null;
				}
			});
		}

		var root = E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, _('Pigo 飞书')),
			E('div', { 'class': 'cbi-map-descr' }, _('扫码绑定飞书机器人，并填写模型 API base、API key 和模型 id。密钥只保存在路由器上。')),
			statusNode,
			E('div', { 'style': 'margin:12px 0' }, [
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'click': ui.createHandlerFn(this, function() {
						return pigo(['bind-begin']).then(function(stdout) {
							showStatus(JSON.parse(stdout));
							return refreshQR();
						}).then(function() {
							if (timer)
								window.clearInterval(timer);
							timer = window.setInterval(function() { poll(); }, 3000);
						}).catch(function(err) {
							ui.addNotification(null, E('p', {}, err.message), 'error');
						});
					})
				}, _('开始扫码绑定')),
				' ',
				E('button', {
					'class': 'cbi-button',
					'click': ui.createHandlerFn(this, function() {
						return readStatus().then(showStatus).catch(function(err) {
							ui.addNotification(null, E('p', {}, err.message), 'error');
						});
					})
				}, _('刷新状态'))
			]),
			qr,
			code,
			E('h3', {}, _('模型')),
			E('div', { 'class': 'cbi-value' }, [E('label', {}, 'API base'), base]),
			E('div', { 'class': 'cbi-value' }, [E('label', {}, _('模型 id')), model]),
			E('div', { 'class': 'cbi-value' }, [E('label', {}, 'API key'), key]),
			hint,
			E('div', { 'style': 'margin-top:12px' }, [
				E('button', {
					'class': 'cbi-button cbi-button-save',
					'click': ui.createHandlerFn(this, function() {
						var args = ['set'];
						if (base.value.trim())
							args.push('--base', base.value.trim());
						if (model.value.trim())
							args.push('--model', model.value.trim());
						if (key.value.trim())
							args.push('--key', key.value.trim());
						if (args.length === 1) {
							ui.addNotification(null, E('p', {}, _('没有要保存的内容')), 'warning');
							return;
						}
						return pigo(args).then(function(stdout) {
							key.value = '';
							showStatus(JSON.parse(stdout));
							ui.addNotification(null, E('p', {}, _('已保存')), 'info');
						}).catch(function(err) {
							ui.addNotification(null, E('p', {}, err.message), 'error');
						});
					})
				}, _('保存'))
			])
		]);

		readStatus().then(showStatus).catch(function(err) {
			statusNode.textContent = err.message;
		});
		return root;
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
