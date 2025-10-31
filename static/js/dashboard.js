// Dashboard for Nyyu Market with Chart.js

let isOnline = false;
let candleChart = null;
let publishChart = null;

// Parse Prometheus metrics
function parseMetrics(text) {
    const metrics = {};
    const lines = text.split('\n');

    for (const line of lines) {
        if (line.startsWith('#') || line.trim() === '') continue;

        // Parse: metric_name{labels} value
        const match = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([\d.e+-]+)$/);
        if (match) {
            const [, name, labels, value] = match;
            if (!metrics[name]) metrics[name] = [];

            // Parse labels
            const labelObj = {};
            const labelPairs = labels.match(/(\w+)="([^"]*)"/g);
            if (labelPairs) {
                labelPairs.forEach(pair => {
                    const [key, val] = pair.split('=');
                    labelObj[key] = val.replace(/"/g, '');
                });
            }

            metrics[name].push({ labels: labelObj, value: parseFloat(value) });
        } else {
            // Parse: metric_name value (no labels)
            const simpleMatch = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)\s+([\d.e+-]+)$/);
            if (simpleMatch) {
                const [, name, value] = simpleMatch;
                if (!metrics[name]) metrics[name] = [];
                metrics[name].push({ labels: {}, value: parseFloat(value) });
            }
        }
    }

    return metrics;
}

// Fetch and display metrics
async function fetchMetrics() {
    try {
        const response = await fetch('/metrics');

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const text = await response.text();
        const metrics = parseMetrics(text);

        console.log('Fetched metrics:', Object.keys(metrics).slice(0, 10)); // Debug: show first 10 metric names
        console.log('Total metrics:', Object.keys(metrics).length);

        updateDashboard(metrics);
        updateStatus(true);

    } catch (error) {
        console.error('Error fetching metrics:', error);
        updateStatus(false);
    }
}

// Fetch exchange status
async function fetchExchangeStatus() {
    try {
        const response = await fetch('/api/v1/realtime/status');
        if (response.ok) {
            const result = await response.json();
            console.log('Exchange status response:', result); // Debug

            // The API returns: { success: true, data: { exchanges: {...} } }
            if (result.data && result.data.exchanges) {
                updateExchangeList(result.data);
            }
        } else {
            console.log('Exchange status endpoint returned:', response.status);
        }
    } catch (error) {
        console.error('Error fetching exchange status:', error);
    }
}

// Update all dashboard components
function updateDashboard(metrics) {
    updateQuickStats(metrics);
    updateCharts(metrics);
    updateSystemInfo(metrics);
}

// Update quick stats cards
function updateQuickStats(metrics) {
    // Latency (from realtime candle latency histogram)
    const latency = metrics['nyyu_realtime_candle_latency_ms'];
    if (latency && latency.length > 0) {
        // Find the sum and count from histogram to calculate average
        const sumMetric = latency.find(m => m.labels.quantile === undefined);
        if (sumMetric) {
            document.getElementById('latencyValue').textContent = sumMetric.value.toFixed(2) + ' ms';
        } else {
            // Fallback: just show first value
            document.getElementById('latencyValue').textContent = latency[0].value.toFixed(2) + ' ms';
        }
    }

    // Price Updates
    const priceUpdates = metrics['nyyu_price_updates_total'];
    if (priceUpdates && priceUpdates.length > 0) {
        const total = priceUpdates.reduce((sum, m) => sum + m.value, 0);
        document.getElementById('priceUpdatesValue').textContent = formatNumber(total);
    }

    // Cache Hit Ratio
    const cacheHits = metrics['nyyu_cache_hits_total'];
    const cacheMisses = metrics['nyyu_cache_misses_total'];
    if (cacheHits && cacheMisses && cacheHits.length > 0 && cacheMisses.length > 0) {
        const hits = cacheHits.reduce((sum, m) => sum + m.value, 0);
        const misses = cacheMisses.reduce((sum, m) => sum + m.value, 0);
        const ratio = hits / (hits + misses) * 100;
        document.getElementById('cacheRatioValue').textContent = ratio.toFixed(1) + '%';
    }

    // Memory
    const memStats = metrics['nyyu_memory_allocated_bytes'];
    if (memStats && memStats.length > 0) {
        const memMB = memStats[0].value / 1024 / 1024;
        document.getElementById('memoryValue').textContent = memMB.toFixed(1) + ' MB';
    } else {
        // Fallback to Go runtime metrics
        const goMemStats = metrics['go_memstats_alloc_bytes'];
        if (goMemStats && goMemStats.length > 0) {
            const memMB = goMemStats[0].value / 1024 / 1024;
            document.getElementById('memoryValue').textContent = memMB.toFixed(1) + ' MB';
        }
    }
}

// Update charts
function updateCharts(metrics) {
    updateCandleChart(metrics);
    updatePublishChart(metrics);
}

// Update candle updates by exchange chart
function updateCandleChart(metrics) {
    const candleUpdates = metrics['nyyu_realtime_candle_updates_total'];
    if (!candleUpdates) {
        console.log('No nyyu_realtime_candle_updates_total metric found');
        console.log('Available metrics with "candle":', Object.keys(metrics).filter(k => k.includes('candle')));
        return;
    }

    console.log('Candle updates data:', candleUpdates.slice(0, 3)); // Debug: show first 3 entries

    // Group by exchange (normalize to uppercase for consistency)
    const exchangeData = {};
    candleUpdates.forEach(m => {
        const exchange = (m.labels.exchange || 'unknown').toUpperCase();
        if (!exchangeData[exchange]) exchangeData[exchange] = 0;
        exchangeData[exchange] += m.value;
    });

    // Sort by value descending
    const sortedEntries = Object.entries(exchangeData).sort((a, b) => b[1] - a[1]);
    const labels = sortedEntries.map(e => e[0]);
    const data = sortedEntries.map(e => e[1]);

    console.log('Exchange candle data:', exchangeData); // Debug: show all exchanges

    // Color palette for each exchange
    const colors = [
        'rgba(139, 92, 246, 0.8)',  // Purple
        'rgba(236, 72, 153, 0.8)',  // Pink
        'rgba(59, 130, 246, 0.8)',  // Blue
        'rgba(16, 185, 129, 0.8)',  // Green
        'rgba(251, 146, 60, 0.8)',  // Orange
        'rgba(239, 68, 68, 0.8)',   // Red
    ];

    const ctx = document.getElementById('candleUpdatesChart');
    if (!ctx) return;

    if (candleChart) {
        candleChart.data.labels = labels;
        candleChart.data.datasets[0].data = data;
        candleChart.data.datasets[0].backgroundColor = colors.slice(0, labels.length);
        candleChart.update();
    } else {
        candleChart = new Chart(ctx, {
            type: 'bar',
            data: {
                labels: labels,
                datasets: [{
                    label: 'Candle Updates',
                    data: data,
                    backgroundColor: colors.slice(0, labels.length),
                    borderColor: colors.slice(0, labels.length).map(c => c.replace('0.8', '1')),
                    borderWidth: 2
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                plugins: {
                    legend: { display: false }
                },
                scales: {
                    y: {
                        beginAtZero: true,
                        ticks: {
                            color: '#9ca3af',
                            callback: function(value) {
                                return formatNumber(value);
                            }
                        },
                        grid: {
                            color: 'rgba(255, 255, 255, 0.1)'
                        }
                    },
                    x: {
                        ticks: {
                            color: '#9ca3af'
                        },
                        grid: {
                            color: 'rgba(255, 255, 255, 0.1)'
                        }
                    }
                }
            }
        });
    }
}

// Update publish success vs failures chart
function updatePublishChart(metrics) {
    const publishSuccess = metrics['nyyu_publish_success_total'];
    const publishFailures = metrics['nyyu_publish_failures_total'];

    let successCount = 0;
    let failureCount = 0;

    if (publishSuccess) {
        successCount = publishSuccess.reduce((sum, m) => sum + m.value, 0);
    }
    if (publishFailures) {
        failureCount = publishFailures.reduce((sum, m) => sum + m.value, 0);
    }

    const ctx = document.getElementById('publishChart');
    if (!ctx) return;

    if (publishChart) {
        publishChart.data.datasets[0].data = [successCount, failureCount];
        publishChart.update();
    } else {
        publishChart = new Chart(ctx, {
            type: 'doughnut',
            data: {
                labels: ['Success', 'Failures'],
                datasets: [{
                    data: [successCount, failureCount],
                    backgroundColor: [
                        'rgba(16, 185, 129, 0.8)',
                        'rgba(239, 68, 68, 0.8)'
                    ],
                    borderColor: [
                        'rgba(16, 185, 129, 1)',
                        'rgba(239, 68, 68, 1)'
                    ],
                    borderWidth: 2
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                plugins: {
                    legend: {
                        position: 'bottom'
                    }
                }
            }
        });
    }
}

// Update system info
function updateSystemInfo(metrics) {
    // Version (from health endpoint)
    fetch('/health')
        .then(r => {
            if (!r.ok) {
                console.log('Health endpoint returned:', r.status);
                throw new Error('Health endpoint not ok');
            }
            return r.json();
        })
        .then(data => {
            console.log('Health data:', data); // Debug
            document.getElementById('versionValue').textContent = data.version || '--';
            document.getElementById('uptimeValue').textContent = formatUptime(data.uptime_seconds);

            // Database status
            const clickhouse = document.getElementById('clickhouseValue');
            clickhouse.textContent = data.clickhouse_healthy ? 'Healthy' : 'Unhealthy';
            clickhouse.className = 'px-3 py-1 rounded-full text-xs font-semibold ' +
                (data.clickhouse_healthy ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800');

            const redis = document.getElementById('redisValue');
            redis.textContent = data.redis_healthy ? 'Healthy' : 'Unhealthy';
            redis.className = 'px-3 py-1 rounded-full text-xs font-semibold ' +
                (data.redis_healthy ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800');

            // Total candles
            if (data.candles_count !== undefined) {
                document.getElementById('totalCandlesValue').textContent = formatNumber(data.candles_count);
            }
        })
        .catch(err => console.error('Error fetching health:', err));

    // Goroutines
    const goroutines = metrics['nyyu_goroutines_active'];
    if (goroutines && goroutines.length > 0) {
        document.getElementById('goroutinesValue').textContent = formatNumber(goroutines[0].value);
    } else {
        // Fallback to Go runtime metrics
        const goGoroutines = metrics['go_goroutines'];
        if (goGoroutines && goGoroutines.length > 0) {
            document.getElementById('goroutinesValue').textContent = formatNumber(goGoroutines[0].value);
        }
    }
}

// Update exchange list
function updateExchangeList(statusData) {
    const container = document.getElementById('exchangeList');
    if (!container || !statusData.exchanges) {
        console.log('No exchange data available');
        return;
    }

    container.innerHTML = '';

    Object.entries(statusData.exchanges).forEach(([exchange, data]) => {
        const item = document.createElement('div');
        item.className = 'flex justify-between items-center p-4 bg-black bg-opacity-30 rounded-lg border border-gray-700';

        // is_healthy indicates if exchange is connected and working
        const isOnline = data.is_healthy || false;
        const msgCount = data.msg_count || 0;
        const errorCount = data.error_count || 0;

        item.innerHTML = `
            <span class="text-sm font-semibold text-white">${exchange.toUpperCase()}</span>
            <div class="flex items-center gap-2">
                <span class="w-2 h-2 rounded-full ${isOnline ? 'bg-green-500' : 'bg-red-500'}"></span>
                <span class="text-xs text-gray-300">${formatNumber(msgCount)} msgs</span>
                ${errorCount > 0 ? `<span class="text-xs text-red-400">${errorCount} errors</span>` : ''}
            </div>
        `;

        container.appendChild(item);
    });
}

// Update status indicator
function updateStatus(online) {
    const indicator = document.getElementById('statusIndicator');
    const text = document.getElementById('statusText');

    if (online !== isOnline) {
        isOnline = online;

        if (online) {
            indicator.className = 'w-3 h-3 rounded-full bg-green-500 animate-pulse-dot';
            text.textContent = 'Online';
        } else {
            indicator.className = 'w-3 h-3 rounded-full bg-red-500';
            text.textContent = 'Offline';
        }
    }
}

// Update last update time
function updateLastUpdateTime() {
    const now = new Date();
    const timeString = now.toLocaleTimeString('en-US', {
        hour12: false,
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
    });
    document.getElementById('lastUpdate').textContent = timeString;
}

// Format numbers with K/M suffixes
function formatNumber(num) {
    if (num >= 1000000) {
        return (num / 1000000).toFixed(1) + 'M';
    } else if (num >= 1000) {
        return (num / 1000).toFixed(1) + 'K';
    }
    return Math.round(num).toString();
}

// Format uptime
function formatUptime(seconds) {
    if (!seconds) return '--';

    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);

    if (days > 0) {
        return `${days}d ${hours}h`;
    } else if (hours > 0) {
        return `${hours}h ${minutes}m`;
    } else {
        return `${minutes}m`;
    }
}

// Refresh all data
async function refreshAll() {
    updateLastUpdateTime();
    await Promise.all([
        fetchMetrics(),
        fetchExchangeStatus()
    ]);
}

// Initialize
async function init() {
    await refreshAll();

    // Auto-refresh every 5 seconds
    setInterval(refreshAll, 5000);
}

// Start when page loads
document.addEventListener('DOMContentLoaded', init);
